package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Pi transcript support (format v3, documented at
// https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/session-format.md
// and https://pi.dev/docs/latest/message-types).
//
// A pi file is one JSON object per line:
//
//   - First line: {"type":"session","version":3,"id":"<uuid>",...}
//     (metadata only).
//   - Every other entry extends {type, id, parentId, timestamp} and the
//     entries form a tree: resuming a branch appends in place instead of
//     starting a new file.
//
// Entry types that carry stats:
//
//   - {"type":"message","message":{"role":"assistant","content":[
//     {"type":"toolCall","id","name","arguments"}], "usage":{"input",
//     "output","cacheRead","cacheWrite",...}}} — one turn, usage counted
//     once per message (pi assistant messages are complete, not
//     per-block chunks like Claude Code's).
//   - {"type":"message","message":{"role":"toolResult","toolCallId",
//     "toolName","content":[...],"isError":bool}} — a failed result
//     counts under its tool; its nested `usage` (model work done by
//     the tool itself) is also counted.
//   - {"type":"message","message":{"role":"bashExecution","command",
//     "output","exitCode"}} — the TUI `!` command; one Bash call,
//     failed when exitCode is non-zero.
//   - {"type":"usage","kind","usage":{...}} — model-attributed usage
//     outside any assistant message (e.g. cache warming); counted.
//   - {"type":"compaction"/"branch_summary","usage":{...}} — usage spent
//     generating the summary; counted. `tokensBefore` is a point-in-time
//     figure, not an amount spent, and is never added.
//
// Only the active branch is counted: entries are walked from the last
// entry in the file back to the root via parentId, and entries off that
// path (abandoned branches) are ignored. Counting every branch would
// count the same work twice. Compaction does not delete entries in pi,
// so pre-compaction entries on the active branch are counted: they are
// work the session actually did, even though the model no longer sees
// them (see docs/DECISIONS.md).

// piUsage is pi's Usage shape (camelCase, unlike Claude's *_tokens).
type piUsage struct {
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	Input      int `json:"input"`
	Output     int `json:"output"`
}

func (u *piUsage) tokens() Tokens {
	if u == nil {
		return Tokens{}
	}
	return Tokens{In: u.Input, Out: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

// piMessage is the AgentMessage inside a pi `message` entry.
type piMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Usage      *piUsage        `json:"usage"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    bool            `json:"isError"`
	Command    string          `json:"command"`
	Output     string          `json:"output"`
	ExitCode   *int            `json:"exitCode"`
}

// piBlock is one content block of a pi assistant message.
type piBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// piEntry is one line of a pi session file.
type piEntry struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	ParentID  *string    `json:"parentId"`
	Timestamp string     `json:"timestamp"`
	Message   *piMessage `json:"message"`
	Usage     *piUsage   `json:"usage"`
	Kind      string     `json:"kind"`
}

// piToolNames maps pi's lowercase tool names to the capitalised names
// used in `calls`, so one session's Bash calls are counted under "Bash"
// whether the session was driven by Claude Code or by pi. Unknown names
// pass through unchanged.
var piToolNames = map[string]string{
	"bash":  "Bash",
	"read":  "Read",
	"write": "Write",
	"edit":  "Edit",
	"grep":  "Grep",
	"glob":  "Glob",
}

func normalizePiToolName(name string) string {
	if n, ok := piToolNames[name]; ok {
		return n
	}
	return name
}

// parsePiFile reads one pi session file and returns its stats.
func parsePiFile(path, root string) (*SessionStats, int, error) {
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
	var entries []piEntry
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
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e piEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			skipped++
			continue
		}
		if e.Type == "" {
			skipped++
			continue
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return b.finalize(), skipped, nil
	}
	// Session id comes from the header line.
	for _, e := range entries {
		if e.Type == "session" && e.ID != "" {
			b.id = e.ID
			break
		}
	}
	// Resolve the active branch: walk parentId from the last entry
	// back to the root. Guard against cycles and dangling links.
	byID := make(map[string]int, len(entries))
	for i, e := range entries {
		if e.ID != "" {
			byID[e.ID] = i
		}
	}
	active := make(map[int]bool, len(entries))
	cur := len(entries) - 1
	for steps := 0; steps <= len(entries); steps++ {
		if cur < 0 || cur >= len(entries) || active[cur] {
			break
		}
		active[cur] = true
		p := entries[cur].ParentID
		if p == nil || *p == "" {
			break
		}
		next, ok := byID[*p]
		if !ok {
			break
		}
		cur = next
	}
	for i, e := range entries {
		if !active[i] {
			continue // abandoned branch: not counted
		}
		switch e.Type {
		case "session":
			// Header only; id already taken above.
		case "message":
			if e.Message == nil {
				continue
			}
			switch e.Message.Role {
			case "assistant":
				b.turns++
				b.addTokens(e.Message.Usage.tokens())
				var blocks []piBlock
				if len(e.Message.Content) > 0 {
					_ = json.Unmarshal(e.Message.Content, &blocks)
				}
				for _, blk := range blocks {
					if blk.Type == "toolCall" {
						b.recordToolUse(normalizePiToolName(blk.Name), blk.ID, blk.Arguments, root)
					}
				}
			case "toolResult":
				b.recordToolResult(e.Message.ToolCallID, normalizePiToolName(e.Message.ToolName), e.Message.IsError, extractResultText(e.Message.Content))
				b.addTokens(e.Message.Usage.tokens())
			case "bashExecution":
				args, _ := json.Marshal(map[string]string{"command": e.Message.Command})
				b.recordToolUse("Bash", e.ID, args, root)
				if e.Message.ExitCode != nil && *e.Message.ExitCode != 0 {
					b.recordToolResult(e.ID, "Bash", true, e.Message.Output)
				}
			}
		case "usage":
			// Usage outside any assistant message.
			b.addTokens(e.Usage.tokens())
		case "compaction", "branch_summary":
			// Usage spent generating the summary.
			b.addTokens(e.Usage.tokens())
		}
	}
	return b.finalize(), skipped, nil
}
