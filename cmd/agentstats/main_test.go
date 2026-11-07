// CLI tests for cmd/agentstats.
//
// These run the real built binary as a subprocess (not the run()
// function in-process) so they cover argument parsing, exit codes,
// stdout JSON, the --out file, the --request envelope and the stderr
// telemetry line exactly as a user or the Arabica adapter sees them.
// They exist because v0.1.0 had 0% coverage here: every CLI behaviour
// was only checked by hand.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/daxpublicfirst-arch/agentstats/internal/transcript"
)

var buildOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "agentstats-cli-test-*")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "agentstats")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	_ = out
	return bin, nil
})

func cliBinary(t *testing.T) string {
	t.Helper()
	bin, err := buildOnce()
	if err != nil {
		t.Fatalf("could not build agentstats binary for CLI tests: %v", err)
	}
	return bin
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(cliBinary(t), args...)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("could not run agentstats %v: %v", args, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// telemetryLine parses the one stderr JSON line and checks its shape.
func telemetryLine(t *testing.T, stderr, wantCommand string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 {
		t.Fatalf("stderr = %d lines, want exactly 1 telemetry line:\n%s", len(lines), stderr)
	}
	var tel map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &tel); err != nil {
		t.Fatalf("stderr telemetry is not JSON: %v\n%s", err, stderr)
	}
	if tel["tool"] != "agentstats" {
		t.Fatalf("telemetry tool = %v, want agentstats", tel["tool"])
	}
	if tel["command"] != wantCommand {
		t.Fatalf("telemetry command = %v, want %s", tel["command"], wantCommand)
	}
	if tel["version"] != transcript.Version {
		t.Fatalf("telemetry version = %v, want %s", tel["version"], transcript.Version)
	}
}

func fixtureDir(name string) string {
	return filepath.Join("..", "..", "fixtures", name)
}

func TestCLIVersion(t *testing.T) {
	stdout, stderr, code := runCLI(t, "version")
	if code != 0 {
		t.Fatalf("version exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if stdout != "agentstats "+transcript.Version+"\n" {
		t.Fatalf("version stdout = %q, want %q", stdout, "agentstats "+transcript.Version+"\n")
	}
	telemetryLine(t, stderr, "version")
}

func TestCLIVersionJSON(t *testing.T) {
	stdout, stderr, code := runCLI(t, "--json", "version")
	if code != 0 {
		t.Fatalf("--json version exit = %d, want 0", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("version --json is not JSON: %v", err)
	}
	if v["ok"] != true || v["version"] != transcript.Version || v["tool"] != "agentstats" {
		t.Fatalf("version --json = %v", v)
	}
	telemetryLine(t, stderr, "version")
}

func TestCLIHelp(t *testing.T) {
	stdout, _, code := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "agentstats session") {
		t.Fatalf("--help output does not contain usage:\n%s", stdout)
	}
}

func TestCLINoArgs(t *testing.T) {
	stdout, stderr, code := runCLI(t)
	if code != 2 {
		t.Fatalf("no-args exit = %d, want 2", code)
	}
	var env transcript.ErrorEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("no-args stdout is not an error envelope: %v", err)
	}
	if env.OK || env.Error.Code != "USAGE" {
		t.Fatalf("no-args envelope = %+v, want ok:false code USAGE", env)
	}
	telemetryLine(t, stderr, "unknown")
}

func TestCLIUnknownCommand(t *testing.T) {
	stdout, _, code := runCLI(t, "frobnicate")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	var env transcript.ErrorEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unknown command stdout is not an error envelope: %v", err)
	}
	if env.Error.Code != "USAGE" {
		t.Fatalf("unknown command envelope = %+v, want code USAGE", env)
	}
}

func TestCLISelfTest(t *testing.T) {
	stdout, stderr, code := runCLI(t, "self-test")
	if code != 0 {
		t.Fatalf("self-test exit = %d, want 0 (stdout: %s)", code, stdout)
	}
	var res transcript.SelfTestResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("self-test stdout is not JSON: %v", err)
	}
	if !res.OK {
		t.Fatalf("self-test ok = false: %+v", res.Checks)
	}
	telemetryLine(t, stderr, "self-test")
}

func TestCLIDoctor(t *testing.T) {
	stdout, stderr, code := runCLI(t, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0 (stdout: %s)", code, stdout)
	}
	var res transcript.DoctorResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("doctor stdout is not JSON: %v", err)
	}
	if !res.OK {
		t.Fatalf("doctor ok = false: %+v", res.Checks)
	}
	telemetryLine(t, stderr, "doctor")
}

func TestCLISessionBasic(t *testing.T) {
	stdout, stderr, code := runCLI(t, "session", fixtureDir("basic"), "--json")
	if code != 0 {
		t.Fatalf("session exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	var out transcript.Output
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("session stdout is not JSON: %v", err)
	}
	if !out.OK || out.Totals.Sessions != 1 || out.Skipped != 0 {
		t.Fatalf("session output = %+v, want ok sessions=1 skipped=0", out)
	}
	if out.Version != transcript.Version {
		t.Fatalf("session version = %q, want %q", out.Version, transcript.Version)
	}
	telemetryLine(t, stderr, "session")
}

func TestCLISessionFlagForm(t *testing.T) {
	_, _, code := runCLI(t, "session", "--session", fixtureDir("basic"))
	if code != 0 {
		t.Fatalf("session --session exit = %d, want 0", code)
	}
}

func TestCLISessionEmptyDir(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, code := runCLI(t, "session", dir, "--json")
	if code != 1 {
		t.Fatalf("empty dir exit = %d, want 1 (stdout: %s)", code, stdout)
	}
	var out transcript.Output
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("empty dir stdout is not JSON: %v", err)
	}
	if !out.OK || out.Totals.Sessions != 0 {
		t.Fatalf("empty dir output = %+v, want ok sessions=0", out)
	}
	telemetryLine(t, stderr, "session")
}

func TestCLISessionMissingDir(t *testing.T) {
	stdout, _, code := runCLI(t, "session", filepath.Join(t.TempDir(), "nope"), "--json")
	if code != 2 {
		t.Fatalf("missing dir exit = %d, want 2", code)
	}
	var env transcript.ErrorEnvelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("missing dir stdout is not an error envelope: %v", err)
	}
	if env.Error.Code != "NO_TRANSCRIPT" {
		t.Fatalf("missing dir envelope = %+v, want code NO_TRANSCRIPT", env)
	}
}

func TestCLISessionBadFlag(t *testing.T) {
	_, _, code := runCLI(t, "session", fixtureDir("basic"), "--nope")
	if code != 2 {
		t.Fatalf("bad flag exit = %d, want 2", code)
	}
}

func TestCLISessionOutDir(t *testing.T) {
	outDir := t.TempDir()
	stdout, _, code := runCLI(t, "session", fixtureDir("basic"), "--out", outDir)
	if code != 0 {
		t.Fatalf("session --out exit = %d, want 0", code)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "agentstats.json"))
	if err != nil {
		t.Fatalf("agentstats.json was not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != strings.TrimSpace(stdout) {
		t.Fatalf("--out file differs from stdout:\nfile: %s\nstdout: %s", data, stdout)
	}
}

func TestCLICompare(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.json")
	b := filepath.Join(dir, "b.json")
	// Compare needs two session outputs; build them from the two
	// fixture files directly.
	outA, _, codeA := runCLI(t, "session", filepath.Join(fixtureDir("two"), "a.jsonl"), "--json")
	if codeA != 0 {
		t.Fatalf("session a.jsonl exit = %d, want 0", codeA)
	}
	if err := os.WriteFile(a, []byte(outA), 0o644); err != nil {
		t.Fatal(err)
	}
	outB2, _, codeB := runCLI(t, "session", filepath.Join(fixtureDir("two"), "b.jsonl"), "--json")
	if codeB != 0 {
		t.Fatalf("session b.jsonl exit = %d, want 0", codeB)
	}
	if err := os.WriteFile(b, []byte(outB2), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runCLI(t, "compare", a, b)
	if code != 0 {
		t.Fatalf("compare exit = %d, want 0 (stdout: %s)", code, stdout)
	}
	var cmp transcript.CompareOutput
	if err := json.Unmarshal([]byte(stdout), &cmp); err != nil {
		t.Fatalf("compare stdout is not JSON: %v", err)
	}
	if !cmp.OK {
		t.Fatalf("compare ok = false: %s", stdout)
	}
	telemetryLine(t, stderr, "compare")
}

func TestCLITop(t *testing.T) {
	stdout, stderr, code := runCLI(t, "top", fixtureDir("two"), "--by", "calls")
	if code != 0 {
		t.Fatalf("top exit = %d, want 0 (stdout: %s)", code, stdout)
	}
	var top transcript.TopOutput
	if err := json.Unmarshal([]byte(stdout), &top); err != nil {
		t.Fatalf("top stdout is not JSON: %v", err)
	}
	if !top.OK || top.Total.Sessions != 2 {
		t.Fatalf("top output = %+v, want ok sessions=2", top)
	}
	telemetryLine(t, stderr, "top")
}

func TestCLITopBadBy(t *testing.T) {
	_, _, code := runCLI(t, "top", fixtureDir("two"), "--by", "bogus")
	if code != 2 {
		t.Fatalf("top --by bogus exit = %d, want 2", code)
	}
}

func TestCLIRequestEnvelope(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "req.json")
	env := `{"protocol":"arabica-plugin-v1","command":"session","args":{"session":"` + fixtureDir("basic") + `"}}`
	if err := os.WriteFile(envPath, []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	stdoutEnv, _, code := runCLI(t, "--request", envPath)
	if code != 0 {
		t.Fatalf("--request exit = %d, want 0 (stdout: %s)", code, stdoutEnv)
	}
	stdoutFlags, _, _ := runCLI(t, "session", fixtureDir("basic"), "--json")
	if strings.TrimSpace(stdoutEnv) != strings.TrimSpace(stdoutFlags) {
		t.Fatalf("--request output differs from flags output:\nenvelope: %s\nflags: %s", stdoutEnv, stdoutFlags)
	}
}
