// Command agentstats reads a Claude Code or pi JSONL transcript and
// reports where the session spent calls, tokens, time and errors, and
// which commands it repeated.
//
// It is harness-independent, makes no model calls and uses no network.
// Usage: agentstats <command> [flags]
//
//	agentstats session <dir> [--json] [--out <dir>]
//	agentstats compare <a.json> <b.json> [--json]
//	agentstats top <dir> --by calls|tokens|errors [--limit <n>] [--json]
//	agentstats self-test
//	agentstats doctor
//	agentstats version
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daxpublicfirst-arch/agentstats/internal/transcript"
)

// telemetry is one JSON line written to stderr per run.
type telemetry struct {
	Command     string `json:"command"`
	DurationMs  int64  `json:"duration_ms"`
	InputBytes  int64  `json:"input_bytes"`
	OK          bool   `json:"ok"`
	OutputBytes int    `json:"output_bytes"`
	Tool        string `json:"tool"`
	Version     string `json:"version"`
}

type requestEnvelope struct {
	Protocol string `json:"protocol"`
	Command  string `json:"command"`
	Args     struct {
		Session string `json:"session"`
		Out     string `json:"out"`
	} `json:"args"`
}

const usageText = `agentstats 0.1.1 — read a Claude Code or pi JSONL transcript and report calls, tokens, errors and repeated commands.

Usage:
  agentstats session <dir> [--json] [--out <dir>]
  agentstats session --session <dir> [--out <dir>]
  agentstats compare <a.json> <b.json> [--json]
  agentstats top <dir> --by calls|tokens|errors [--limit <n>] [--json]
  agentstats self-test
  agentstats doctor
  agentstats version
  agentstats --version | --self-test | --help
  agentstats --request <path>   read an arabica-plugin-v1 envelope instead of flags

Exit codes: 0 success, 1 no transcript found (empty answer, not an error),
2 usage or input error, 3 tool failure.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	start := time.Now()
	command := ""
	inputBytes := int64(0)
	outputBytes := 0
	ok := true
	defer func() {
		if command == "" {
			command = "unknown"
		}
		t := telemetry{
			Command:     command,
			DurationMs:  time.Since(start).Milliseconds(),
			InputBytes:  inputBytes,
			OK:          ok,
			OutputBytes: outputBytes,
			Tool:        transcript.ToolName,
			Version:     transcript.Version,
		}
		if b, err := json.Marshal(t); err == nil {
			fmt.Fprintln(os.Stderr, string(b))
		}
	}()

	// Global flags that may appear anywhere.
	jsonOut := false
	var requestPath string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "--request":
			if i+1 >= len(args) {
				return fail(&ok, "USAGE", "missing value for --request",
					"pass a path to an arabica-plugin-v1 request envelope JSON file",
					"run `agentstats --help` for usage", 2)
			}
			i++
			requestPath = args[i]
		case "--help", "-h", "help":
			fmt.Print(usageText)
			return 0
		case "--version":
			command = "version"
			return printVersion(jsonOut, &outputBytes)
		case "--self-test":
			command = "self-test"
			return runSelfTest(&ok, &outputBytes)
		default:
			rest = append(rest, args[i])
		}
	}
	args = rest

	// Optional adapter envelope replaces flags.
	if requestPath != "" {
		env, err := readEnvelope(requestPath)
		if err != nil {
			return fail(&ok, "INVALID_INPUT", err.Error(),
				"pass a valid arabica-plugin-v1 request envelope JSON file",
				"build the envelope with protocol, command and args, or use flags instead", 2)
		}
		if env.Command == "" {
			return fail(&ok, "INVALID_INPUT", "request envelope has no command",
				"set command to session, compare, top, self-test, doctor or version",
				"run `agentstats --help` for usage", 2)
		}
		command = env.Command
		switch env.Command {
		case "session":
			if env.Args.Session == "" {
				return fail(&ok, "USAGE", "request envelope has no args.session",
					"set args.session to the transcript directory or .jsonl file",
					"run `agentstats --help` for usage", 2)
			}
			return runSession(env.Args.Session, env.Args.Out, jsonOut, &command, &inputBytes, &outputBytes, &ok)
		case "self-test":
			return runSelfTest(&ok, &outputBytes)
		case "doctor":
			return runDoctor(&ok, &outputBytes)
		case "version":
			return printVersion(jsonOut, &outputBytes)
		default:
			return fail(&ok, "USAGE", "unsupported command in request envelope: "+env.Command,
				"use session, self-test, doctor or version in the envelope",
				"run `agentstats --help` for usage", 2)
		}
	}

	if len(args) == 0 {
		// Print only the failure envelope on stdout: the plugin
		// contract is one JSON object per run, and printing the
		// usage text first would make stdout unparseable. The
		// envelope's fix points at --help for humans.
		return fail(&ok, "USAGE", "no command given",
			"run one of: session, compare, top, self-test, doctor, version",
			"run `agentstats --help` for usage", 2)
	}
	command = args[0]
	switch command {
	case "version":
		return printVersion(jsonOut, &outputBytes)
	case "self-test":
		return runSelfTest(&ok, &outputBytes)
	case "doctor":
		return runDoctor(&ok, &outputBytes)
	case "session":
		dir, outDir, err := parseSessionArgs(args[1:])
		if err != nil {
			return fail(&ok, "USAGE", err.Error(),
				"usage: agentstats session <dir> [--json] [--out <dir>]",
				"run `agentstats --help` for usage", 2)
		}
		return runSession(dir, outDir, jsonOut, &command, &inputBytes, &outputBytes, &ok)
	case "compare":
		a, b, err := parseCompareArgs(args[1:])
		if err != nil {
			return fail(&ok, "USAGE", err.Error(),
				"usage: agentstats compare <a.json> <b.json> [--json]",
				"run `agentstats --help` for usage", 2)
		}
		cmp, n, err := transcript.CompareFiles(a, b)
		if err != nil {
			return failToolErr(&ok, err)
		}
		inputBytes = n
		return printJSON(cmp, &outputBytes)
	case "top":
		dir, by, limit, err := parseTopArgs(args[1:])
		if err != nil {
			return fail(&ok, "USAGE", err.Error(),
				"usage: agentstats top <dir> --by calls|tokens|errors [--limit <n>] [--json]",
				"run `agentstats --help` for usage", 2)
		}
		out, n, found, err := transcript.AnalyzePath(dir)
		if err != nil {
			return failToolErr(&ok, err)
		}
		inputBytes = n
		if !found {
			// No transcript: empty answer, exit 1 (not an error).
			top, terr := transcript.Top(out, by, limit)
			if terr != nil {
				return failToolErr(&ok, terr)
			}
			code := printJSON(top, &outputBytes)
			if code == 0 {
				return 1
			}
			return code
		}
		top, terr := transcript.Top(out, by, limit)
		if terr != nil {
			return failToolErr(&ok, terr)
		}
		return printJSON(top, &outputBytes)
	default:
		return fail(&ok, "USAGE", "unknown command: "+command,
			"run one of: session, compare, top, self-test, doctor, version",
			"run `agentstats --help` for usage", 2)
	}
}

func readEnvelope(path string) (*requestEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read request file %s: %w", path, err)
	}
	var env requestEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("request file %s is not valid JSON: %w", path, err)
	}
	if env.Protocol != "" && env.Protocol != "arabica-plugin-v1" {
		return nil, fmt.Errorf("request file %s uses unknown protocol %q", path, env.Protocol)
	}
	return &env, nil
}

func parseSessionArgs(args []string) (dir, outDir string, err error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session", "--dir":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("missing value for %s", args[i])
			}
			i++
			dir = args[i]
		case "--out":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("missing value for --out")
			}
			i++
			outDir = args[i]
		case "--json":
			// Already consumed globally; tolerate it here too.
		default:
			if strings.HasPrefix(args[i], "--") {
				return "", "", fmt.Errorf("unknown flag: %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if dir == "" {
		if len(positional) == 0 {
			return "", "", fmt.Errorf("missing session directory")
		}
		dir = positional[0]
	}
	if len(positional) > 1 {
		return "", "", fmt.Errorf("unexpected argument: %s", positional[1])
	}
	return dir, outDir, nil
}

func parseCompareArgs(args []string) (string, string, error) {
	var positional []string
	for _, a := range args {
		if a == "--json" {
			continue
		}
		if strings.HasPrefix(a, "--") {
			return "", "", fmt.Errorf("unknown flag: %s", a)
		}
		positional = append(positional, a)
	}
	if len(positional) != 2 {
		return "", "", fmt.Errorf("compare needs exactly two output files")
	}
	return positional[0], positional[1], nil
}

func parseTopArgs(args []string) (dir, by string, limit int, err error) {
	by = "calls"
	limit = 20
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--by":
			if i+1 >= len(args) {
				return "", "", 0, fmt.Errorf("missing value for --by")
			}
			i++
			by = args[i]
		case "--limit":
			if i+1 >= len(args) {
				return "", "", 0, fmt.Errorf("missing value for --limit")
			}
			i++
			n, convErr := strconv.Atoi(args[i])
			if convErr != nil || n < 0 {
				return "", "", 0, fmt.Errorf("invalid --limit value: %s", args[i])
			}
			limit = n
		case "--json":
		default:
			if strings.HasPrefix(args[i], "--") {
				return "", "", 0, fmt.Errorf("unknown flag: %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if len(positional) == 0 {
		return "", "", 0, fmt.Errorf("missing session directory")
	}
	if len(positional) > 1 {
		return "", "", 0, fmt.Errorf("unexpected argument: %s", positional[1])
	}
	return positional[0], by, limit, nil
}

func runSession(dir, outDir string, jsonOut bool, command *string, inputBytes *int64, outputBytes *int, ok *bool) int {
	*command = "session"
	out, n, found, err := transcript.AnalyzePath(dir)
	if err != nil {
		return failToolErr(ok, err)
	}
	*inputBytes = n
	if !found {
		// No transcript found: an empty answer, not an error.
		code := printJSON(out, outputBytes)
		if code == 0 {
			return 1
		}
		return code
	}
	_ = jsonOut // session output is always one JSON object
	if outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fail(ok, "WRITE_ERROR", "could not create output directory "+outDir+": "+err.Error(),
				"pass a writable --out directory",
				"omit --out to print the result to stdout instead", 3)
		}
		data, err := json.Marshal(out)
		if err != nil {
			return fail(ok, "WRITE_ERROR", "could not encode output: "+err.Error(),
				"re-run the command",
				"omit --out to print the result to stdout instead", 3)
		}
		dst := filepath.Join(outDir, "agentstats.json")
		if err := os.WriteFile(dst, append(data, '\n'), 0o644); err != nil {
			return fail(ok, "WRITE_ERROR", "could not write "+dst+": "+err.Error(),
				"pass a writable --out directory",
				"omit --out to print the result to stdout instead", 3)
		}
	}
	return printJSON(out, outputBytes)
}

func runSelfTest(ok *bool, outputBytes *int) int {
	result := transcript.SelfTest()
	code := printJSON(result, outputBytes)
	if code != 0 {
		*ok = false
		return code
	}
	if !result.OK {
		*ok = false
		return 3
	}
	return 0
}

func runDoctor(ok *bool, outputBytes *int) int {
	result := transcript.Doctor()
	code := printJSON(result, outputBytes)
	if code != 0 {
		*ok = false
		return code
	}
	if !result.OK {
		*ok = false
		return 3
	}
	return 0
}

func printVersion(jsonOut bool, outputBytes *int) int {
	if jsonOut {
		return printJSON(map[string]any{
			"ok": true, "tool": transcript.ToolName, "version": transcript.Version,
		}, outputBytes)
	}
	s := transcript.ToolName + " " + transcript.Version + "\n"
	fmt.Print(s)
	*outputBytes = len(s)
	return 0
}

func printJSON(v any, outputBytes *int) int {
	data, err := json.Marshal(v)
	if err != nil {
		fmt.Println(`{"ok":false,"error":{"blocked":"could not encode output","code":"WRITE_ERROR","fix":"re-run the command","if_that_fails":"report the failing input"}}`)
		return 3
	}
	fmt.Println(string(data))
	*outputBytes = len(data) + 1
	return 0
}

func fail(ok *bool, code, blocked, fix, ifThatFails string, exitCode int) int {
	*ok = false
	env := transcript.ErrorEnvelope{
		Error: transcript.ErrorDetail{Blocked: blocked, Code: code, Fix: fix, IfThatFails: ifThatFails},
		OK:    false,
	}
	data, _ := json.Marshal(env)
	fmt.Println(string(data))
	return exitCode
}

func failToolErr(ok *bool, err error) int {
	if te, isToolErr := err.(*transcript.ToolError); isToolErr {
		return fail(ok, te.Code, te.Blocked, te.Fix, te.IfThatFails, te.ExitCode)
	}
	return fail(ok, "TOOL_FAILURE", err.Error(),
		"re-run the command",
		"check the transcript directory and try a single .jsonl file", 3)
}
