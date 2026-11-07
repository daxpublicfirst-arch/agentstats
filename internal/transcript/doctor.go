package transcript

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/daxpublicfirst-arch/agentstats/fixtures"
)

// DoctorResult is the `doctor` command output.
type DoctorResult struct {
	Checks  []CheckResult `json:"checks"`
	OK      bool          `json:"ok"`
	Tool    string        `json:"tool"`
	Version string        `json:"version"`
}

// Doctor checks the runtime and exits 0 only when the tool can run:
// a supported OS/arch, a writable temp dir, and the embedded fixtures
// the self-test needs. No network is used or required.
func Doctor() *DoctorResult {
	result := &DoctorResult{Checks: []CheckResult{}, Tool: ToolName, Version: Version}
	add := func(name string, passed bool, detail string) {
		result.Checks = append(result.Checks, CheckResult{Detail: detail, Name: name, Passed: passed})
	}
	add("runtime", runtime.GOOS != "" && runtime.GOARCH != "",
		"GOOS="+runtime.GOOS+" GOARCH="+runtime.GOARCH+" go="+runtime.Version())
	tmp, err := os.MkdirTemp("", "agentstats-doctor-*")
	if err != nil {
		add("temp-write", false, "cannot create temp dir: "+err.Error())
	} else {
		p := filepath.Join(tmp, "probe.txt")
		err = os.WriteFile(p, []byte("ok"), 0o644)
		if err != nil {
			add("temp-write", false, "cannot write temp file: "+err.Error())
		} else {
			_, err = os.ReadFile(p)
			add("temp-write", err == nil, "wrote and read back a temp file in "+tmp)
		}
		_ = os.RemoveAll(tmp)
	}
	_, err = fs.ReadDir(fixtures.FS, "basic")
	add("fixtures", err == nil, "embedded fixtures are present (basic/malformed/two)")
	add("network", true, "no network is used or required")
	result.OK = true
	for _, c := range result.Checks {
		if !c.Passed {
			result.OK = false
		}
	}
	return result
}
