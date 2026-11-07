package transcript

import (
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
