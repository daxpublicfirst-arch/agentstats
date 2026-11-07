// Package transcript reads Claude Code / pi JSONL transcripts and
// summarises where a session spent calls, tokens, time and errors.
//
// It is harness-independent, makes no model calls and uses no network:
// it only reads local .jsonl files.
//
// Two transcript shapes are supported; see docs/DESIGN.md for the full
// rationale and sources:
//
//   - Claude Code: one JSON object per line under ~/.claude/projects/.
//     One API message is several `assistant` lines (one per content
//     block) sharing `message.id` and repeating the same `usage`.
//     After /compact, earlier lines are re-appended as exact copies
//     with the same `uuid`.
//   - Pi (v3): a `session` header line, then tree entries linked by
//     `id`/`parentId`. Only the active branch (walked from the last
//     entry) is counted.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Version is the tool version (semver, without a leading "v").
const Version = "0.1.0"

// ToolName is the tool name used in output, telemetry and plugin.toml.
const ToolName = "agentstats"

// MaxInputBytes is the plugin limit: at most 500 MiB of transcript input.
const MaxInputBytes = 500 * 1024 * 1024

// MaxDuration is the plugin limit: a run should finish within 60s.
const MaxDuration = 60 * time.Second

// maxLineBytes caps one transcript line. A line longer than this is
// skipped and counted in `skipped`, never loaded whole into memory and
// never fatal to the file. Real transcript lines are far smaller:
// Claude Code truncates tool-result text to ~10 KiB by default and
// ~1 MiB at most. It is a variable (not a constant) so tests can lower
// it without generating a 16 MiB line.
var maxLineBytes = 16 * 1024 * 1024

// SortedMap is a string->int map. encoding/json marshals Go maps with
// keys sorted, which is what deterministic output requires.
type SortedMap map[string]int

// Tokens counts tokens by category.
type Tokens struct {
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	In         int `json:"in"`
	Out        int `json:"out"`
}

// TokensDelta is a delta of Tokens; zero fields are omitted so a compare
// only shows categories that actually changed.
type TokensDelta struct {
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	In         int `json:"in,omitempty"`
	Out        int `json:"out,omitempty"`
}

// RepeatedCmd is a Bash command seen more than once, after normalisation.
type RepeatedCmd struct {
	Command string `json:"command"`
	Count   int    `json:"count"`
}

// FileCount is a file path and how many times it was read/searched.
type FileCount struct {
	Count int    `json:"count"`
	Path  string `json:"path"`
}

// ErrorCount is a distinct failed tool result, truncated to 200 chars.
type ErrorCount struct {
	Count   int    `json:"count"`
	Message string `json:"message"`
	Tool    string `json:"tool"`
}

// SessionStats is the per-file summary in a session output.
type SessionStats struct {
	Calls    SortedMap     `json:"calls"`
	Errors   []ErrorCount  `json:"errors"`
	Failed   SortedMap     `json:"failed"`
	File     string        `json:"file"`
	ID       string        `json:"id"`
	Repeated []RepeatedCmd `json:"repeated"`
	Tokens   Tokens        `json:"tokens"`
	TopFiles []FileCount   `json:"top_files"`
	Turns    int           `json:"turns"`
}

// Totals aggregates every session in a session output.
type Totals struct {
	Calls    SortedMap `json:"calls"`
	Failed   SortedMap `json:"failed"`
	Sessions int       `json:"sessions"`
	Tokens   Tokens    `json:"tokens"`
	Turns    int       `json:"turns"`
}

// Output is the top-level `session` command output.
type Output struct {
	OK       bool           `json:"ok"`
	Sessions []SessionStats `json:"sessions"`
	Skipped  int            `json:"skipped"`
	Tool     string         `json:"tool"`
	Totals   Totals         `json:"totals"`
	Version  string         `json:"version"`
}

// ErrorDetail is the inner object of the failure envelope.
type ErrorDetail struct {
	Blocked     string `json:"blocked"`
	Code        string `json:"code"`
	Fix         string `json:"fix"`
	IfThatFails string `json:"if_that_fails"`
}

// ErrorEnvelope is printed (one JSON object) on failure.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
	OK    bool        `json:"ok"`
}

// ToolError is a failure that maps to the error envelope and an exit code.
type ToolError struct {
	Blocked     string
	Code        string
	ExitCode    int
	Fix         string
	IfThatFails string
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Blocked }

func newToolError(code, blocked, fix, ifThatFails string, exitCode int) *ToolError {
	return &ToolError{Blocked: blocked, Code: code, ExitCode: exitCode, Fix: fix, IfThatFails: ifThatFails}
}

func emptyOutput() *Output {
	return &Output{
		OK:       true,
		Sessions: []SessionStats{},
		Skipped:  0,
		Tool:     ToolName,
		Totals: Totals{
			Calls:  SortedMap{},
			Failed: SortedMap{},
		},
		Version: Version,
	}
}

// --- raw transcript shapes ---

type rawUsage struct {
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	In            int `json:"input_tokens"`
	Out           int `json:"output_tokens"`
}

func tokensFromUsage(u *rawUsage) Tokens {
	if u == nil {
		return Tokens{}
	}
	return Tokens{In: u.In, Out: u.Out, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}
}

type rawBlock struct {
	Content   json.RawMessage `json:"content"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	IsError   bool            `json:"is_error"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Type      string          `json:"type"`
}

// rawMessage is a Claude Code message. Its `content` is either an array
// of content blocks or a plain string (user prompts are usually a
// string; tool results are always an array). The custom unmarshaller
// accepts both; a string carries no blocks.
type rawMessage struct {
	Content []rawBlock `json:"-"`
	ID      string     `json:"id"`
	Usage   *rawUsage  `json:"usage"`
}

func (m *rawMessage) UnmarshalJSON(data []byte) error {
	var a struct {
		Content json.RawMessage `json:"content"`
		ID      string          `json:"id"`
		Usage   *rawUsage       `json:"usage"`
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	m.ID = a.ID
	m.Usage = a.Usage
	trimmed := bytes.TrimSpace(a.Content)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		m.Content = nil
		return nil
	}
	var arr []rawBlock
	if err := json.Unmarshal(a.Content, &arr); err == nil {
		m.Content = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(a.Content, &s); err == nil {
		// Plain-text content: no blocks to count.
		m.Content = nil
		return nil
	}
	return fmt.Errorf("message.content is neither an array nor a string")
}

type rawLine struct {
	Message   *rawMessage `json:"message"`
	SessionID string      `json:"sessionId"`
	Timestamp string      `json:"timestamp"`
	Type      string      `json:"type"`
	Usage     *rawUsage   `json:"usage"`
	UUID      string      `json:"uuid"`
}

// toolInput is the subset of tool-call arguments agentstats reads.
// Keys cover both Claude Code (file_path, path, pattern, command) and
// pi (filePath) spellings, and both object and JSON-encoded-string
// inputs (see parseToolInput).
type toolInput struct {
	Command     string `json:"command"`
	FilePath    string `json:"file_path"`
	FilePathAlt string `json:"filePath"`
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
}

// parseToolInput reads a tool-call input that is either a JSON object
// (local Claude Code transcripts) or a JSON-encoded string holding an
// object (the shape the Compliance API returns).
func parseToolInput(raw json.RawMessage) toolInput {
	var in toolInput
	if len(bytes.TrimSpace(raw)) == 0 {
		return in
	}
	if err := json.Unmarshal(raw, &in); err == nil {
		return in
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		_ = json.Unmarshal([]byte(s), &in)
	}
	return in
}

// lineReader reads one line at a time with a bounded per-line size.
// It replaces bufio.Scanner, whose hard token limit fails the whole
// file on one over-long line and cannot resume after it. Lines longer
// than maxLineBytes are reported as tooLong and fully consumed, so the
// caller can skip them and continue with the next line.
type lineReader struct {
	r *bufio.Reader
}

func newLineReader(f *os.File) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(f, 64*1024)}
}

// next returns the next line without its trailing newline. ok is false
// at end of file. tooLong means the line exceeded maxLineBytes; the
// rest of that line has been consumed. err is a real read error only.
func (lr *lineReader) next() (line string, tooLong bool, ok bool, err error) {
	var buf []byte
	for {
		frag, fragErr := lr.r.ReadSlice('\n')
		if len(buf)+len(frag) > maxLineBytes {
			// Over the cap: stop keeping bytes and discard the
			// rest of this line, up to its newline.
			if fragErr == nil {
				return "", true, true, nil
			}
			if fragErr == bufio.ErrBufferFull {
				continue
			}
			if fragErr == io.EOF {
				return "", true, false, nil
			}
			return "", true, false, fragErr
		}
		buf = append(buf, frag...)
		switch fragErr {
		case nil:
			return strings.TrimRight(string(buf), "\r\n"), false, true, nil
		case io.EOF:
			if len(buf) == 0 {
				return "", false, false, nil
			}
			return strings.TrimRight(string(buf), "\r\n"), false, true, nil
		case bufio.ErrBufferFull:
			continue
		default:
			return "", false, false, fragErr
		}
	}
}

// DiscoverFiles returns the .jsonl files under path, sorted, recursing at
// most two levels of subdirectories. path may be a single .jsonl file.
// It also returns the total input size in bytes.
func DiscoverFiles(path string) ([]string, int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, 0, newToolError("NO_TRANSCRIPT",
			"no transcript found at "+path,
			"pass a project directory containing .jsonl files, or a single .jsonl file",
			"check the path exists and contains .jsonl transcript files", 2)
	}
	if !info.IsDir() {
		if strings.HasSuffix(strings.ToLower(abs), ".jsonl") {
			return []string{abs}, info.Size(), nil
		}
		return nil, 0, newToolError("NO_TRANSCRIPT",
			"no transcript found at "+path,
			"pass a project directory containing .jsonl files, or a single .jsonl file",
			"check the path exists and contains .jsonl transcript files", 2)
	}
	var files []string
	var total int64
	root := abs
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			// Skip hidden directories (.git, .claude, ...).
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			// Recurse at most two levels: root/sub (depth 1) and
			// root/sub/sub (depth 2). Do not descend below depth 2.
			// Depth 2 also covers Claude Code subagent transcripts
			// at <session-id>/subagents/*.jsonl.
			if strings.Count(rel, string(os.PathSeparator)) >= 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		total += fi.Size()
		files = append(files, p)
		return nil
	})
	if err != nil {
		return nil, 0, newToolError("READ_ERROR",
			"could not read transcript directory "+path+": "+err.Error(),
			"check the directory is readable",
			"try a single .jsonl file instead of a directory", 3)
	}
	if total > MaxInputBytes {
		return nil, total, newToolError("INPUT_TOO_LARGE",
			fmt.Sprintf("transcript input is %d bytes, over the 500 MiB limit", total),
			"analyse a smaller session directory or a single .jsonl file",
			"split the transcript directory into smaller parts", 2)
	}
	sort.Strings(files)
	return files, total, nil
}

// AnalyzePath analyses one directory or one .jsonl file. found is false
// when the path exists but contains no .jsonl transcript at all (an empty
// answer, not an error: the caller exits 1 with the empty output).
func AnalyzePath(path string) (*Output, int64, bool, error) {
	start := time.Now()
	files, totalBytes, err := DiscoverFiles(path)
	if err != nil {
		return nil, 0, false, err
	}
	if len(files) == 0 {
		return emptyOutput(), 0, false, nil
	}
	abs, _ := filepath.Abs(path)
	root := abs
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		root = filepath.Dir(abs)
	}
	out := emptyOutput()
	out.Totals.Calls = SortedMap{}
	out.Totals.Failed = SortedMap{}
	for _, f := range files {
		if time.Since(start) > MaxDuration {
			return nil, totalBytes, true, newToolError("TIMEOUT",
				"analysis exceeded the 60s limit",
				"analyse a smaller session directory or a single .jsonl file",
				"split the transcript directory into smaller parts", 3)
		}
		sess, skipped, err := analyzeFile(f, root)
		if err != nil {
			return nil, totalBytes, true, err
		}
		out.Sessions = append(out.Sessions, *sess)
		out.Skipped += skipped
		out.Totals.Sessions++
		out.Totals.Turns += sess.Turns
		out.Totals.Tokens.In += sess.Tokens.In
		out.Totals.Tokens.Out += sess.Tokens.Out
		out.Totals.Tokens.CacheRead += sess.Tokens.CacheRead
		out.Totals.Tokens.CacheWrite += sess.Tokens.CacheWrite
		for k, v := range sess.Calls {
			out.Totals.Calls[k] += v
		}
		for k, v := range sess.Failed {
			out.Totals.Failed[k] += v
		}
	}
	return out, totalBytes, true, nil
}

// sessionBuilder accumulates one file's stats.
type sessionBuilder struct {
	id        string
	file      string
	turns     int
	tokens    Tokens
	calls     SortedMap
	failed    SortedMap
	seenIDs   map[string]bool
	seenNoID  map[string]bool
	idToName  map[string]string
	msgSeen   map[string]bool   // assistant message.id already counted as a turn
	msgUsage  map[string]Tokens // last usage seen per assistant message.id
	bashCount map[string]int
	fileCount map[string]int
	errCount  map[string]int // key: tool + "\x00" + message
	errTool   map[string]string
	errMsg    map[string]string
}

func newSessionBuilder(file string) *sessionBuilder {
	return &sessionBuilder{
		file:      file,
		calls:     SortedMap{},
		failed:    SortedMap{},
		seenIDs:   map[string]bool{},
		seenNoID:  map[string]bool{},
		idToName:  map[string]string{},
		msgSeen:   map[string]bool{},
		msgUsage:  map[string]Tokens{},
		bashCount: map[string]int{},
		fileCount: map[string]int{},
		errCount:  map[string]int{},
		errTool:   map[string]string{},
		errMsg:    map[string]string{},
	}
}

func (b *sessionBuilder) addUsage(u *rawUsage) {
	if u == nil {
		return
	}
	t := tokensFromUsage(u)
	b.tokens.In += t.In
	b.tokens.Out += t.Out
	b.tokens.CacheRead += t.CacheRead
	b.tokens.CacheWrite += t.CacheWrite
}

// addTokens adds already-decoded token counts (used by the pi reader,
// whose usage fields are camelCase: input/output/cacheRead/cacheWrite).
func (b *sessionBuilder) addTokens(t Tokens) {
	b.tokens.In += t.In
	b.tokens.Out += t.Out
	b.tokens.CacheRead += t.CacheRead
	b.tokens.CacheWrite += t.CacheWrite
}

// countAssistant counts one assistant message as a turn and records its
// usage. A Claude Code API message is written as several `assistant`
// lines (one per content block) that share `message.id` and repeat the
// same `usage`: lines with an id count one turn together, and only the
// last line's usage is kept (in background-agent transcripts the usage
// grows block by block and only the final line has the full figures).
// Lines without an id (pi-shaped or older files) count one turn each.
func (b *sessionBuilder) countAssistant(msgID string, u *rawUsage) {
	if msgID == "" {
		b.turns++
		b.addUsage(u)
		return
	}
	if !b.msgSeen[msgID] {
		b.msgSeen[msgID] = true
		b.turns++
	}
	if u != nil {
		b.msgUsage[msgID] = tokensFromUsage(u)
	}
}

// recordToolUse counts one tool call, deduplicated by tool_use.id: an
// api-request-blob repeats a call that also appears in an assistant
// event, so each unique call is counted once. Calls without an id fall
// back to a name+input key.
func (b *sessionBuilder) recordToolUse(name, id string, input json.RawMessage, root string) {
	if id != "" {
		if b.seenIDs[id] {
			return
		}
		b.seenIDs[id] = true
		b.idToName[id] = name
	} else {
		h := name + "\x00" + string(input)
		if b.seenNoID[h] {
			return
		}
		b.seenNoID[h] = true
	}
	if name == "" {
		return
	}
	b.calls[name]++
	in := parseToolInput(input)
	switch name {
	case "Bash":
		if in.Command != "" {
			b.bashCount[NormalizeCommand(in.Command, root)]++
		}
	case "Read":
		if p := firstNonEmpty(in.FilePath, in.FilePathAlt, in.Path); p != "" {
			b.fileCount[p]++
		}
	case "Grep":
		// Grep's "pattern" is a search regex, not a path;
		// only its "path" counts as a file.
		if p := firstNonEmpty(in.Path, in.FilePath, in.FilePathAlt); p != "" {
			b.fileCount[p]++
		}
	case "Glob":
		if in.Pattern != "" {
			b.fileCount[in.Pattern]++
		}
	}
}

// recordToolResult counts one failed tool result. toolName is the name
// carried by the result itself when present (pi toolResult messages);
// otherwise the tool is looked up from the tool_use.id that produced
// it, and "unknown" when that id was never seen.
func (b *sessionBuilder) recordToolResult(toolUseID, toolName string, isError bool, message string) {
	if !isError {
		return
	}
	tool := toolName
	if tool == "" {
		tool = b.idToName[toolUseID]
	}
	if tool == "" {
		tool = "unknown"
	}
	b.failed[tool]++
	if len(message) > 200 {
		message = message[:200]
	}
	key := tool + "\x00" + message
	b.errCount[key]++
	b.errTool[key] = tool
	b.errMsg[key] = message
}

func (b *sessionBuilder) processBlocks(blocks []rawBlock, root string) {
	for _, blk := range blocks {
		switch blk.Type {
		case "tool_use", "server_tool_use":
			// server_tool_use is the Anthropic block for built-in
			// server tools (e.g. web search); it carries the same
			// id/name/input shape as tool_use.
			b.recordToolUse(blk.Name, blk.ID, blk.Input, root)
		case "tool_result":
			b.recordToolResult(blk.ToolUseID, blk.Name, blk.IsError, extractResultText(blk.Content))
		}
	}
}

func (b *sessionBuilder) finalize() *SessionStats {
	id := b.id
	if id == "" {
		base := filepath.Base(b.file)
		id = strings.TrimSuffix(base, filepath.Ext(base))
	}
	// Usage kept per assistant message.id is summed here, once per
	// distinct message (see countAssistant).
	for _, t := range b.msgUsage {
		b.tokens.In += t.In
		b.tokens.Out += t.Out
		b.tokens.CacheRead += t.CacheRead
		b.tokens.CacheWrite += t.CacheWrite
	}
	return &SessionStats{
		Calls:    b.calls,
		Errors:   topErrors(b.errCount, b.errTool, b.errMsg, 20),
		Failed:   b.failed,
		File:     b.file,
		ID:       id,
		Repeated: topRepeated(b.bashCount, 20),
		Tokens:   b.tokens,
		TopFiles: topFiles(b.fileCount, 20),
		Turns:    b.turns,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// extractResultText pulls plain text out of a tool_result content value,
// which may be a string, an array of {type:"text",text:"..."} blocks,
// a single {text:"..."} object, or anything else (raw JSON as fallback).
func extractResultText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var arr []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var parts []string
		for _, p := range arr {
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Text != "" {
		return strings.TrimSpace(obj.Text)
	}
	return strings.TrimSpace(string(raw))
}

// processClaudeLine handles one Claude-shaped line. It returns true when
// the line was skipped (unparsable or unusable) and should be counted in
// `skipped`. Lines re-appended by /compact (same `uuid` as a line already
// seen) are ignored silently: they are exact duplicates, not errors.
func processClaudeLine(b *sessionBuilder, line, root string, seenUUIDs map[string]bool) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	var raw rawLine
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return true
	}
	if raw.Type == "" && raw.Message == nil {
		return true
	}
	if raw.UUID != "" {
		if seenUUIDs[raw.UUID] {
			return false
		}
		seenUUIDs[raw.UUID] = true
	}
	if raw.SessionID != "" && b.id == "" {
		b.id = raw.SessionID
	}
	switch raw.Type {
	case "assistant":
		// Turns count assistant messages, not tool calls and not
		// streaming chunks: see countAssistant.
		if raw.Message != nil {
			usage := raw.Message.Usage
			if usage == nil {
				usage = raw.Usage
			}
			b.countAssistant(raw.Message.ID, usage)
			b.processBlocks(raw.Message.Content, root)
		} else {
			b.countAssistant("", raw.Usage)
		}
	case "api-request-blob":
		// Duplicates an assistant call; dedupe by tool_use.id,
		// count no turn and no tokens for it.
		if raw.Message != nil {
			b.processBlocks(raw.Message.Content, root)
		}
	case "user":
		if raw.Message != nil {
			b.processBlocks(raw.Message.Content, root)
		}
	case "result":
		// A result row repeats the usage of the assistant
		// messages that produced it; counting it would
		// double-count tokens. Ignore it.
	default:
		// Other line types (system, summary, file-history-snapshot,
		// queue-operation, ...) carry no countable stats. Lines
		// with a message and content are read generically, so
		// future shapes still contribute their tool calls.
		if raw.Message != nil {
			b.processBlocks(raw.Message.Content, root)
		}
	}
	return false
}

func analyzeFile(path, root string) (*SessionStats, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, newToolError("READ_ERROR",
			"could not read transcript file "+path+": "+err.Error(),
			"check the file is readable",
			"try a different transcript file", 3)
	}
	defer f.Close()
	abs, _ := filepath.Abs(path)
	b := newSessionBuilder(abs)
	skipped := 0
	r := newLineReader(f)
	// Peek at the first line: pi files start with a `session` header
	// line, Claude Code files never do.
	first, tooLong, ok, err := r.next()
	if err != nil {
		return nil, 0, newToolError("READ_ERROR",
			"could not read transcript file "+path+": "+err.Error(),
			"check the file is not truncated or corrupt",
			"try a different transcript file", 3)
	}
	if ok && !tooLong {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(first)), &probe) == nil && probe.Type == "session" {
			// Pi v3 file: parse it with the pi reader instead.
			return parsePiFile(path, root)
		}
	}
	seenUUIDs := map[string]bool{}
	if ok {
		if tooLong {
			skipped++
		} else if processClaudeLine(b, first, root, seenUUIDs) {
			skipped++
		}
	}
	for {
		line, tooLong, ok, err := r.next()
		if err != nil {
			return nil, skipped, newToolError("READ_ERROR",
				"could not read transcript file "+path+": "+err.Error(),
				"check the file is not truncated or corrupt",
				"try a different transcript file", 3)
		}
		if !ok {
			break
		}
		if tooLong {
			skipped++
			continue
		}
		if processClaudeLine(b, line, root, seenUUIDs) {
			skipped++
		}
	}
	return b.finalize(), skipped, nil
}
