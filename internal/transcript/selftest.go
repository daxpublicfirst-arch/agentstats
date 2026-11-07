package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/daxpublicfirst-arch/agentstats/fixtures"
)

// CheckResult is one self-test check.
type CheckResult struct {
	Detail string `json:"detail"`
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

// SelfTestResult is the `self-test` command output.
type SelfTestResult struct {
	Checks  []CheckResult `json:"checks"`
	OK      bool          `json:"ok"`
	Tool    string        `json:"tool"`
	Version string        `json:"version"`
}

// SelfTest runs every fixture in fixtures/ and reports whether each
// expected output matches exactly (after normalising the temp fixture
// path to "<fixture>"). It exits 0 only when every check passes.
func SelfTest() *SelfTestResult {
	result := &SelfTestResult{
		Checks:  []CheckResult{},
		Tool:    ToolName,
		Version: Version,
	}
	add := func(name string, passed bool, detail string) {
		result.Checks = append(result.Checks, CheckResult{Detail: detail, Name: name, Passed: passed})
		if !passed {
			result.OK = false
		}
	}
	// Session fixtures: basic, malformed, two.
	for _, name := range []string{"basic", "malformed", "two"} {
		dir, err := materialiseFixture(name)
		if err != nil {
			add("session:"+name, false, err.Error())
			continue
		}
		out, _, found, err := AnalyzePath(dir)
		if err != nil {
			add("session:"+name, false, err.Error())
			_ = os.RemoveAll(dir)
			continue
		}
		if !found {
			add("session:"+name, false, "no transcript found in fixture "+name)
			_ = os.RemoveAll(dir)
			continue
		}
		ok, detail := compareJSON(out, name+"/expected.json", dir)
		add("session:"+name, ok, detail)
		_ = os.RemoveAll(dir)
	}
	// Compare fixture: compare expected-a.json with expected-b.json.
	{
		dir, err := os.MkdirTemp("", "agentstats-selftest-compare-*")
		if err != nil {
			add("compare:two", false, err.Error())
		} else {
			aPath := filepath.Join(dir, "expected-a.json")
			bPath := filepath.Join(dir, "expected-b.json")
			_ = writeFixtureFile("two/expected-a.json", aPath)
			_ = writeFixtureFile("two/expected-b.json", bPath)
			cmp, _, err := CompareFiles(aPath, bPath)
			if err != nil {
				add("compare:two", false, err.Error())
			} else {
				ok, detail := compareJSON(cmp, "two/expected-compare.json", dir)
				add("compare:two", ok, detail)
			}
			_ = os.RemoveAll(dir)
		}
	}
	if len(result.Checks) > 0 {
		result.OK = true
		for _, c := range result.Checks {
			if !c.Passed {
				result.OK = false
			}
		}
	}
	return result
}

// materialiseFixture copies a fixture's .jsonl files into a fresh temp dir
// and returns the temp dir path.
func materialiseFixture(name string) (string, error) {
	dir, err := os.MkdirTemp("", "agentstats-selftest-"+name+"-*")
	if err != nil {
		return "", err
	}
	prefix := name
	err = fs.WalkDir(fixtures.FS, prefix, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		return writeFixtureFile(p, filepath.Join(dir, filepath.Base(p)))
	})
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func writeFixtureFile(src, dst string) error {
	data, err := fixtures.FS.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// compareJSON marshals v, replaces the temp dir with "<fixture>", and
// deep-compares it with the embedded expected JSON.
func compareJSON(v any, expectedPath, tmpDir string) (bool, string) {
	actual, err := json.Marshal(v)
	if err != nil {
		return false, "marshal: " + err.Error()
	}
	actual = bytes.ReplaceAll(actual, []byte(tmpDir), []byte("<fixture>"))
	expected, err := fixtures.FS.ReadFile(expectedPath)
	if err != nil {
		return false, "read expected: " + err.Error()
	}
	var a, b any
	if err := json.Unmarshal(actual, &a); err != nil {
		return false, "parse actual: " + err.Error()
	}
	if err := json.Unmarshal(expected, &b); err != nil {
		return false, "parse expected: " + err.Error()
	}
	if !reflect.DeepEqual(a, b) {
		return false, fmt.Sprintf("mismatch:\nactual:   %s\nexpected: %s", string(actual), strings.TrimSpace(string(expected)))
	}
	return true, "output matches expected.json exactly"
}
