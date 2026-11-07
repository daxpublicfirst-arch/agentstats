package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeTempJSONL(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDedupeBlobDoesNotDoubleCount(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"git status"}}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}},"sessionId":"s1"}
{"type":"api-request-blob","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"git status"}}]}}
{"type":"api-request-blob","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"git status"}}]}}
`)
	out, _, found, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected a transcript to be found")
	}
	if got := out.Totals.Calls["Bash"]; got != 1 {
		t.Fatalf("Bash calls = %d, want 1 (blob duplicates must not count)", got)
	}
	if got := out.Totals.Turns; got != 1 {
		t.Fatalf("turns = %d, want 1", got)
	}
	if got := out.Totals.Tokens.In; got != 10 {
		t.Fatalf("tokens.in = %d, want 10 (blob usage must not count)", got)
	}
	if got := out.Totals.Tokens.CacheRead; got != 100 {
		t.Fatalf("cache_read = %d, want 100", got)
	}
}

func TestResultRowDoesNotDoubleCountTokens(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"a.ts"}}],"usage":{"input_tokens":5,"output_tokens":6}},"sessionId":"s1"}
{"type":"result","subtype":"success","total_cost_usd":0.1,"usage":{"input_tokens":5,"output_tokens":6}}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Totals.Tokens.In; got != 5 {
		t.Fatalf("tokens.in = %d, want 5 (result row must not double-count)", got)
	}
	if got := out.Totals.Tokens.Out; got != 6 {
		t.Fatalf("tokens.out = %d, want 6", got)
	}
}

func TestFailedCountedOnceUnderRightTool(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"false"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Read","input":{"file_path":"a.ts"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"Exit code 1"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu2","is_error":false,"content":"ok"}]}}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Totals.Failed["Bash"]; got != 1 {
		t.Fatalf("failed Bash = %d, want 1", got)
	}
	if _, ok := out.Totals.Failed["Read"]; ok {
		t.Fatalf("failed Read should be absent, got %v", out.Totals.Failed)
	}
	if len(out.Sessions[0].Errors) != 1 || out.Sessions[0].Errors[0].Tool != "Bash" {
		t.Fatalf("errors = %+v, want one Bash error", out.Sessions[0].Errors)
	}
}

func TestMalformedLinesSkippedNotFatal(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"m1","name":"Bash","input":{"command":"echo hi"}}],"usage":{"input_tokens":2,"output_tokens":3}},"sessionId":"s1"}
not json at all
{"type":"assistant", broken
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", out.Skipped)
	}
	if out.Totals.Turns != 1 {
		t.Fatalf("turns = %d, want 1", out.Totals.Turns)
	}
}

func TestRepeatedNormalization(t *testing.T) {
	cases := map[string]string{
		"git   status --short":                                    "git status --short",
		"git status --short -- foo bar":                           "git status --short",
		"git status --short -- ./some/file":                       "git status --short -- ./some/file",
		"curl https://x.example/abcdef0123456789abcdef0123456789": "curl https://x.example/<id>",
	}
	for in, want := range cases {
		if got := NormalizeCommand(in, "/nonexistent-root"); got != want {
			t.Errorf("NormalizeCommand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeCommand("cat /tmp/proj/src/a.ts", "/tmp/proj"); got != "cat <root>/src/a.ts" {
		t.Errorf("root replacement: got %q", got)
	}
}

func TestTopFilesOnlyCountsPaths(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"g1","name":"Grep","input":{"pattern":"TODO","path":"src/"}},{"type":"tool_use","id":"g2","name":"Glob","input":{"pattern":"src/**/*.ts"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := out.Sessions[0].TopFiles
	if len(files) != 2 {
		t.Fatalf("top_files = %+v, want 2 entries (Grep path + Glob pattern, not the Grep regex)", files)
	}
}

func TestCompareDelta(t *testing.T) {
	a := &Output{Totals: Totals{Turns: 10, Tokens: Tokens{Out: 100}, Calls: SortedMap{"Bash": 5}, Failed: SortedMap{"Bash": 2}}}
	b := &Output{Totals: Totals{Turns: 8, Tokens: Tokens{Out: 50}, Calls: SortedMap{"Bash": 3, "Read": 1}, Failed: SortedMap{}}}
	cmp := Compare("a.json", "b.json", a, b)
	if cmp.Delta.Turns != -2 {
		t.Fatalf("delta turns = %d, want -2", cmp.Delta.Turns)
	}
	if cmp.Delta.Tokens.Out != -50 {
		t.Fatalf("delta tokens.out = %d, want -50", cmp.Delta.Tokens.Out)
	}
	if cmp.Delta.Calls["Bash"] != -2 || cmp.Delta.Calls["Read"] != 1 {
		t.Fatalf("delta calls = %v", cmp.Delta.Calls)
	}
	if cmp.Delta.Failed["Bash"] != -2 {
		t.Fatalf("delta failed = %v", cmp.Delta.Failed)
	}
}

func TestNoTranscriptIsEmptyAnswer(t *testing.T) {
	dir := t.TempDir()
	out, _, found, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("found should be false for an empty directory")
	}
	if len(out.Sessions) != 0 || !out.OK {
		t.Fatalf("expected empty ok output, got %+v", out)
	}
}

func TestSelfTestPasses(t *testing.T) {
	res := SelfTest()
	if !res.OK {
		for _, c := range res.Checks {
			t.Logf("check %s passed=%v detail=%s", c.Name, c.Passed, c.Detail)
		}
		t.Fatal("self-test did not pass")
	}
}

func TestDoctorPasses(t *testing.T) {
	res := Doctor()
	if !res.OK {
		for _, c := range res.Checks {
			t.Logf("check %s passed=%v detail=%s", c.Name, c.Passed, c.Detail)
		}
		t.Fatal("doctor did not pass")
	}
}

// One API message is several assistant lines sharing message.id, every
// line repeating the usage. Turns and tokens must be counted once per
// message.id (last usage wins); re-appended lines after /compact (same
// uuid) must not count at all; a user message whose content is a plain
// string must not be counted as skipped.
func TestChunkedAssistantLinesCountOnce(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","uuid":"u-1a","sessionId":"c1","message":{"id":"msg-1","content":[{"type":"thinking","thinking":"hmm"}],"usage":{"input_tokens":10,"output_tokens":20}}}
{"type":"assistant","uuid":"u-1b","sessionId":"c1","message":{"id":"msg-1","content":[{"type":"text","text":"plan"}],"usage":{"input_tokens":20,"output_tokens":40}}}
{"type":"assistant","uuid":"u-1c","sessionId":"c1","message":{"id":"msg-1","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"git status"}}],"usage":{"input_tokens":30,"output_tokens":60}}}
{"type":"assistant","uuid":"u-2","sessionId":"c1","message":{"id":"msg-2","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"a.ts"}}],"usage":{"input_tokens":5,"output_tokens":5}}}
{"type":"assistant","uuid":"u-2","sessionId":"c1","message":{"id":"msg-2","content":[{"type":"tool_use","id":"t2","name":"Read","input":{"file_path":"a.ts"}}],"usage":{"input_tokens":5,"output_tokens":5}}}
{"type":"user","uuid":"u-3","sessionId":"c1","message":{"role":"user","content":"hello"}}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Totals.Turns; got != 2 {
		t.Fatalf("turns = %d, want 2 (one per message.id)", got)
	}
	// Last usage per message.id wins: msg-1 -> 30/60, msg-2 -> 5/5.
	if got := out.Totals.Tokens.In; got != 35 {
		t.Fatalf("tokens.in = %d, want 35 (last usage per message.id)", got)
	}
	if got := out.Totals.Tokens.Out; got != 65 {
		t.Fatalf("tokens.out = %d, want 65", got)
	}
	if got := out.Totals.Calls["Bash"]; got != 1 {
		t.Fatalf("Bash calls = %d, want 1", got)
	}
	if got := out.Totals.Calls["Read"]; got != 1 {
		t.Fatalf("Read calls = %d, want 1 (uuid duplicate must not recount)", got)
	}
	if out.Skipped != 0 {
		t.Fatalf("skipped = %d, want 0 (string content is valid)", out.Skipped)
	}
}

func TestOverLongLineIsSkippedNotFatal(t *testing.T) {
	old := maxLineBytes
	maxLineBytes = 300 // valid lines below are ~210 bytes; the long one is ~600
	defer func() { maxLineBytes = old }()
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s1","name":"Bash","input":{"command":"echo ok"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s2","name":"Bash","input":{"command":"`+string(make([]byte, 500))+`"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"s3","name":"Bash","input":{"command":"echo ok2"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the over-long line)", out.Skipped)
	}
	if got := out.Totals.Turns; got != 2 {
		t.Fatalf("turns = %d, want 2 (lines around the long one still count)", got)
	}
}

func TestToolUseInputAsJSONString(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"j1","name":"Bash","input":"{\"command\":\"git status\"}"}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"j2","name":"Bash","input":"{\"command\":\"git status\"}"}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Totals.Calls["Bash"]; got != 2 {
		t.Fatalf("Bash calls = %d, want 2", got)
	}
	rep := out.Sessions[0].Repeated
	if len(rep) != 1 || rep[0].Command != "git status" || rep[0].Count != 2 {
		t.Fatalf("repeated = %+v, want [{git status 2}] (input was a JSON string)", rep)
	}
}

func TestServerToolUseCounted(t *testing.T) {
	dir := writeTempJSONL(t, `{"type":"assistant","message":{"content":[{"type":"server_tool_use","id":"w1","name":"WebSearch","input":{"query":"x"}}],"usage":{"input_tokens":1,"output_tokens":1}},"sessionId":"s1"}
`)
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Totals.Calls["WebSearch"]; got != 1 {
		t.Fatalf("WebSearch calls = %d, want 1", got)
	}
}

// Parallel parsing must produce byte-identical output to serial
// parsing, even when a message.id group, a repeated tool_use.id or a
// compaction duplicate straddles a chunk boundary. The threshold is
// lowered so a small file takes the parallel path.
func TestParallelMatchesSerial(t *testing.T) {
	old := parallelMinBytes
	defer func() { parallelMinBytes = old }()
	content := `{"type":"assistant","uuid":"p-1a","sessionId":"par","message":{"id":"m1","content":[{"type":"thinking","thinking":"x"}],"usage":{"input_tokens":10,"output_tokens":20}}}
{"type":"assistant","uuid":"p-1b","sessionId":"par","message":{"id":"m1","content":[{"type":"tool_use","id":"k1","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":30,"output_tokens":60}}}
{"type":"assistant","uuid":"p-2","sessionId":"par","message":{"id":"m2","content":[{"type":"tool_use","id":"k2","name":"Read","input":{"file_path":"main.go"}}],"usage":{"input_tokens":5,"output_tokens":5}}}
{"type":"api-request-blob","uuid":"p-blob","sessionId":"par","message":{"content":[{"type":"tool_use","id":"k1","name":"Bash","input":{"command":"go test ./..."}}]}}
{"type":"assistant","uuid":"p-2","sessionId":"par","message":{"id":"m2","content":[{"type":"tool_use","id":"k2","name":"Read","input":{"file_path":"main.go"}}],"usage":{"input_tokens":5,"output_tokens":5}}}
{"type":"user","uuid":"p-u","sessionId":"par","message":{"role":"user","content":"go on"}}
{"type":"assistant","uuid":"p-3","sessionId":"par","message":{"content":[{"type":"tool_use","id":"k3","name":"Bash","input":{"command":"go test ./..."}}],"usage":{"input_tokens":1,"output_tokens":1}}}
not json at all
{"type":"assistant","uuid":"p-4","sessionId":"par","message":{"id":"m3","content":[{"type":"tool_use","id":"k4","name":"Grep","input":{"path":"src","pattern":"TODO"}}],"usage":{"input_tokens":2,"output_tokens":2}}}
`
	dir := writeTempJSONL(t, content)
	parallelMinBytes = 512
	outPar, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	parallelMinBytes = 1 << 62
	outSer, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	jPar, _ := json.Marshal(outPar)
	jSer, _ := json.Marshal(outSer)
	if string(jPar) != string(jSer) {
		t.Fatalf("parallel output differs from serial:\nparallel: %s\nserial:   %s", jPar, jSer)
	}
	// And the expected values themselves (guards against both paths
	// being wrong in the same way).
	if got := outPar.Totals.Turns; got != 4 {
		t.Fatalf("turns = %d, want 4 (m1, m2, no-id, m3)", got)
	}
	if got := outPar.Totals.Tokens.In; got != 38 {
		t.Fatalf("tokens.in = %d, want 38 (30+5+1+2)", got)
	}
	if got := outPar.Totals.Calls["Bash"]; got != 2 {
		t.Fatalf("Bash calls = %d, want 2 (k1 once despite the blob, k3 once)", got)
	}
	if got := outPar.Totals.Calls["Read"]; got != 1 {
		t.Fatalf("Read calls = %d, want 1 (uuid duplicate must not recount)", got)
	}
	if outPar.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1 (the malformed line)", outPar.Skipped)
	}
	// The same check against the real fixtures.
	for _, name := range []string{"basic", "chunked", "two"} {
		parallelMinBytes = 512
		p, _, _, err := AnalyzePath(filepath.Join("..", "..", "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		parallelMinBytes = 1 << 62
		s, _, _, err := AnalyzePath(filepath.Join("..", "..", "fixtures", name))
		if err != nil {
			t.Fatal(err)
		}
		jp, _ := json.Marshal(p)
		js, _ := json.Marshal(s)
		if string(jp) != string(js) {
			t.Fatalf("fixture %s: parallel output differs from serial", name)
		}
	}
}

func TestPiActiveBranchOnly(t *testing.T) {
	dir := t.TempDir()
	content := `{"type":"session","version":3,"id":"pi-t","timestamp":"2026-11-08T00:00:00Z","cwd":"/tmp/p"}
{"type":"message","id":"e1","parentId":null,"timestamp":"2026-11-08T00:00:00Z","message":{"role":"user","content":"hi","timestamp":1}}
{"type":"message","id":"e2","parentId":"e1","timestamp":"2026-11-08T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"make build"}}],"usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0},"timestamp":2}}
{"type":"message","id":"e3","parentId":"e2","timestamp":"2026-11-08T00:00:02Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"bash","content":[{"type":"text","text":"ok"}],"isError":false,"timestamp":3}}
{"type":"message","id":"e4","parentId":"e2","timestamp":"2026-11-08T00:00:03Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c9","name":"bash","arguments":{"command":"rm -rf /"}}],"usage":{"input":999,"output":999,"cacheRead":0,"cacheWrite":0},"timestamp":4}}
{"type":"message","id":"e5","parentId":"e3","timestamp":"2026-11-08T00:00:04Z","message":{"role":"user","content":"done","timestamp":5}}
{"type":"usage","id":"e6","parentId":"e5","timestamp":"2026-11-08T00:00:05Z","kind":"cache_warm","usage":{"input":0,"output":0,"cacheRead":100,"cacheWrite":0}}
`
	if err := os.WriteFile(filepath.Join(dir, "one.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, _, err := AnalyzePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Sessions[0].ID; got != "pi-t" {
		t.Fatalf("session id = %q, want pi-t (from pi header)", got)
	}
	if got := out.Totals.Turns; got != 1 {
		t.Fatalf("turns = %d, want 1 (abandoned branch assistant must not count)", got)
	}
	if got := out.Totals.Tokens.In; got != 10 {
		t.Fatalf("tokens.in = %d, want 10 (999 from the abandoned branch must not count)", got)
	}
	if got := out.Totals.Tokens.CacheRead; got != 100 {
		t.Fatalf("cache_read = %d, want 100 (usage entry counts)", got)
	}
	if got := out.Totals.Calls["Bash"]; got != 1 {
		t.Fatalf("Bash calls = %d, want 1", got)
	}
}
