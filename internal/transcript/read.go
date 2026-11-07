// Package transcript reads Claude Code / pi JSONL transcripts and
// summarises where a session spent calls, tokens, time and errors.
//
// It is harness-independent, makes no model calls and uses no network:
// it only reads local .jsonl files.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
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

type rawBlock struct {
	Content   json.RawMessage `json:"content"`
	ID        string          `json:"id"`
	Input     json.RawMessage `json:"input"`
	IsError   bool            `json:"is_error"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	Type      string          `json:"type"`
}

type rawMessage struct {
	Content []rawBlock `json:"content"`
	Usage   *rawUsage  `json:"usage"`
}

type rawLine struct {
	Message   *rawMessage `json:"message"`
	SessionID string      `json:"sessionId"`
	Timestamp string      `json:"timestamp"`
	Type      string      `json:"type"`
	Usage     *rawUsage   `json:"usage"`
}

type toolInput struct {
	Command  string `json:"command"`
	FilePath string `json:"file_path"`
	Path     string `json:"path"`
	Pattern  string `json:"pattern"`
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
	b.tokens.In += u.In
	b.tokens.Out += u.Out
	b.tokens.CacheRead += u.CacheRead
	b.tokens.CacheWrite += u.CacheCreation
}

func (b *sessionBuilder) processBlocks(blocks []rawBlock, root string) {
	for _, blk := range blocks {
		switch blk.Type {
		case "tool_use":
			// Dedupe by tool_use.id: an api-request-blob repeats a call
			// that also appears in an assistant event. Count each
			// unique call once. Blocks without an id fall back to a
			// hash of name+input.
			key := blk.ID
			if key != "" {
				if b.seenIDs[key] {
					continue
				}
				b.seenIDs[key] = true
				b.idToName[key] = blk.Name
			} else {
				h := blk.Name + "\x00" + string(blk.Input)
				if b.seenNoID[h] {
					continue
				}
				b.seenNoID[h] = true
			}
			if blk.Name == "" {
				continue
			}
			b.calls[blk.Name]++
			var in toolInput
			if len(blk.Input) > 0 {
				_ = json.Unmarshal(blk.Input, &in)
			}
			switch blk.Name {
			case "Bash":
				if in.Command != "" {
					b.bashCount[NormalizeCommand(in.Command, root)]++
				}
			case "Read":
				if p := firstNonEmpty(in.FilePath, in.Path); p != "" {
					b.fileCount[p]++
				}
			case "Grep":
				// Grep's "pattern" is a search regex, not a path;
				// only its "path" counts as a file.
				if in.Path != "" {
					b.fileCount[in.Path]++
				}
			case "Glob":
				if in.Pattern != "" {
					b.fileCount[in.Pattern]++
				}
			}
		case "tool_result":
			if !blk.IsError {
				continue
			}
			tool := b.idToName[blk.ToolUseID]
			if tool == "" {
				tool = "unknown"
			}
			b.failed[tool]++
			msg := extractResultText(blk.Content)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			key := tool + "\x00" + msg
			b.errCount[key]++
			b.errTool[key] = tool
			b.errMsg[key] = msg
		}
	}
}

func (b *sessionBuilder) finalize() *SessionStats {
	id := b.id
	if id == "" {
		base := filepath.Base(b.file)
		id = strings.TrimSuffix(base, filepath.Ext(base))
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
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw rawLine
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			skipped++
			continue
		}
		if raw.Type == "" && raw.Message == nil {
			skipped++
			continue
		}
		if raw.SessionID != "" && b.id == "" {
			b.id = raw.SessionID
		}
		switch raw.Type {
		case "assistant":
			// Turns count assistant messages, not tool calls.
			// Tokens are summed once per assistant message; the
			// usage inside the message is the authoritative row.
			b.turns++
			if raw.Message != nil {
				b.addUsage(raw.Message.Usage)
				if raw.Message.Usage == nil {
					b.addUsage(raw.Usage)
				}
				b.processBlocks(raw.Message.Content, root)
			} else {
				b.addUsage(raw.Usage)
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
			// pi transcripts and future shapes: if a line has a
			// message with content, read tool calls/results from
			// it, but count no turn and no tokens.
			if raw.Message != nil {
				b.processBlocks(raw.Message.Content, root)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, skipped, newToolError("READ_ERROR",
			"could not read transcript file "+path+": "+err.Error(),
			"check the file is not truncated or corrupt",
			"try a different transcript file", 3)
	}
	return b.finalize(), skipped, nil
}
