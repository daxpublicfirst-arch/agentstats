package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// bigBenchOnce builds one ~50 MB transcript (170k assistant lines with
// unique message ids, the worst case for the dedupe maps) and reuses it
// across benchmarks.
var bigBenchOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "agentstats-bench-*")
	if err != nil {
		return "", err
	}
	line := `{"type":"assistant","sessionId":"big","message":{"id":"msg-%d","content":[{"type":"tool_use","id":"t%d","name":"Bash","input":{"command":"git status --short"}}],"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}},"timestamp":"2026-11-08T01:00:00Z"}` + "\n"
	f, err := os.Create(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	for i := 0; i < 170000; i++ {
		if _, err := fmt.Fprintf(f, line, i, i); err != nil {
			return "", err
		}
	}
	return dir, nil
})

func BenchmarkSession50MB(b *testing.B) {
	dir, err := bigBenchOnce()
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, _, err := AnalyzePath(dir); err != nil {
			b.Fatal(err)
		}
	}
}
