package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// Delta is the difference b - a between two session outputs.
type Delta struct {
	Calls  SortedMap   `json:"calls"`
	Failed SortedMap   `json:"failed"`
	Tokens TokensDelta `json:"tokens"`
	Turns  int         `json:"turns"`
}

// CompareOutput is the `compare` command output.
type CompareOutput struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Delta Delta  `json:"delta"`
	OK    bool   `json:"ok"`
}

// TopSession is one row of the `top` command output.
type TopSession struct {
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
	File   string `json:"file"`
	ID     string `json:"id"`
	Tokens Tokens `json:"tokens"`
	Turns  int    `json:"turns"`
}

// TopOutput is the `top` command output. Total is the total row.
type TopOutput struct {
	By       string       `json:"by"`
	Limit    int          `json:"limit"`
	OK       bool         `json:"ok"`
	Sessions []TopSession `json:"sessions"`
	Tool     string       `json:"tool"`
	Total    Totals       `json:"total"`
	Version  string       `json:"version"`
}

// LoadOutput reads a session output JSON file (as produced by `session`).
func LoadOutput(path string) (*Output, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, newToolError("INVALID_INPUT",
			"could not read output file "+path+": "+err.Error(),
			"pass two session output JSON files produced by `agentstats session`",
			"re-run `agentstats session` to produce the output files", 2)
	}
	var out Output
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, newToolError("INVALID_INPUT",
			"file "+path+" is not a valid agentstats session output: "+err.Error(),
			"pass two session output JSON files produced by `agentstats session`",
			"re-run `agentstats session` to produce the output files", 2)
	}
	if out.Totals.Calls == nil {
		out.Totals.Calls = SortedMap{}
	}
	if out.Totals.Failed == nil {
		out.Totals.Failed = SortedMap{}
	}
	return &out, nil
}

// Compare returns the delta b - a. Map entries with a zero delta are
// omitted; token categories with a zero delta are omitted.
func Compare(aPath, bPath string, a, b *Output) *CompareOutput {
	delta := Delta{Calls: SortedMap{}, Failed: SortedMap{}}
	delta.Turns = b.Totals.Turns - a.Totals.Turns
	delta.Tokens.In = b.Totals.Tokens.In - a.Totals.Tokens.In
	delta.Tokens.Out = b.Totals.Tokens.Out - a.Totals.Tokens.Out
	delta.Tokens.CacheRead = b.Totals.Tokens.CacheRead - a.Totals.Tokens.CacheRead
	delta.Tokens.CacheWrite = b.Totals.Tokens.CacheWrite - a.Totals.Tokens.CacheWrite
	for k, bv := range b.Totals.Calls {
		if d := bv - a.Totals.Calls[k]; d != 0 {
			delta.Calls[k] = d
		}
	}
	for k, av := range a.Totals.Calls {
		if _, ok := b.Totals.Calls[k]; !ok && av != 0 {
			delta.Calls[k] = -av
		}
	}
	for k, bv := range b.Totals.Failed {
		if d := bv - a.Totals.Failed[k]; d != 0 {
			delta.Failed[k] = d
		}
	}
	for k, av := range a.Totals.Failed {
		if _, ok := b.Totals.Failed[k]; !ok && av != 0 {
			delta.Failed[k] = -av
		}
	}
	return &CompareOutput{A: aPath, B: bPath, Delta: delta, OK: true}
}

// CompareFiles loads two session output files and compares them.
func CompareFiles(aPath, bPath string) (*CompareOutput, int64, error) {
	a, err := LoadOutput(aPath)
	if err != nil {
		return nil, 0, err
	}
	b, err := LoadOutput(bPath)
	if err != nil {
		return nil, 0, err
	}
	var inputBytes int64
	for _, p := range []string{aPath, bPath} {
		if fi, err := os.Stat(p); err == nil {
			inputBytes += fi.Size()
		}
	}
	return Compare(aPath, bPath, a, b), inputBytes, nil
}

func sumMap(m SortedMap) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func totalTokens(t Tokens) int {
	return t.In + t.Out + t.CacheRead + t.CacheWrite
}

// Top builds the `top` output: sessions sorted by the chosen measure
// (calls = total tool calls, tokens = in+out+cache_read+cache_write,
// errors = total failed calls), descending, then id ascending.
func Top(out *Output, by string, limit int) (*TopOutput, error) {
	switch by {
	case "calls", "tokens", "errors":
	default:
		return nil, newToolError("USAGE",
			"unknown --by value "+fmt.Sprintf("%q", by),
			"use --by calls, --by tokens or --by errors",
			"run `agentstats top --help` for usage", 2)
	}
	rows := make([]TopSession, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		rows = append(rows, TopSession{
			Calls:  sumMap(s.Calls),
			Errors: sumMap(s.Failed),
			File:   s.File,
			ID:     s.ID,
			Tokens: s.Tokens,
			Turns:  s.Turns,
		})
	}
	measure := func(r TopSession) int {
		switch by {
		case "calls":
			return r.Calls
		case "tokens":
			return totalTokens(r.Tokens)
		default:
			return r.Errors
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if measure(rows[i]) != measure(rows[j]) {
			return measure(rows[i]) > measure(rows[j])
		}
		return rows[i].ID < rows[j].ID
	})
	if limit <= 0 {
		limit = 20
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []TopSession{}
	}
	return &TopOutput{
		By:       by,
		Limit:    limit,
		OK:       true,
		Sessions: rows,
		Tool:     ToolName,
		Total:    out.Totals,
		Version:  Version,
	}, nil
}
