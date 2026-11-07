# agentstats — Architecture

## Layout

```text
agentstats/
  go.mod
  cmd/agentstats/main.go          # CLI: flags, envelope, exit codes, telemetry
  cmd/agentstats/main_test.go     # CLI tests: real binary as a subprocess (17 tests)
  internal/transcript/
    read.go                       # Claude Code reader, discovery, builder, parallel parse
    pi.go                         # pi v3 reader
    count.go                      # session totals, top-N lists
    repeat.go                     # Bash command normalisation
    compare.go                    # compare + top commands
    selftest.go                   # --self-test (fixtures vs expected.json)
    doctor.go                     # doctor checks
    transcript_test.go            # unit tests (21 tests)
    bench_test.go                 # BenchmarkSession50MB
  fixtures/{basic,malformed,two,chunked,pi}/
                                  # .jsonl inputs + expected.json outputs
  fixtures/fixtures.go            # embeds fixtures into the binary (go:embed)
  schema/{request,response,error,telemetry}.json
  plugin.toml                     # arabica-plugin-v1
  docs/{DESIGN,ARCHITECTURE,DECISIONS,CI}.md
  docs/ci.yml                     # copy of the CI workflow (see docs/CI.md)
  .github/workflows/ci.yml        # the workflow itself (NOT on main: see docs/CI.md)
  README.md  LICENSE  CHANGELOG.md  Makefile
```

Go, standard library only, no network, no model calls. Go 1.25+.

## Data flow

```
flags / --request envelope
        │
        ▼
cmd/agentstats/main.go  (run) ── parses args, sets command
        │                         prints exactly one JSON object to stdout
        │                         prints one telemetry JSON line to stderr
        ▼
transcript.AnalyzePath(path)
        │
        ├─ DiscoverFiles: walk ≤2 levels, skip hidden dirs, sum sizes,
        │  reject >500 MiB before reading anything
        ├─ per file: analyzeFile
        │     ├─ peek first line → pi header? → parsePiFile (serial)
        │     ├─ file > 32 MiB? → analyzeFileParallel
        │     └─ else → analyzeFileSerial
        └─ aggregate sessions into Output (totals summed)
```

### The builder

`sessionBuilder` (read.go) accumulates one file's stats. It is the
single place where counting rules live, shared by the serial, parallel
and pi paths:

- `countAssistant(msgID, usage, offset)` — turns and per-message usage
  (ADR-04).
- `recordToolUse(name, id, input, root)` — dedupe by id, calls,
  Bash/file counting.
- `recordToolResult(toolUseID, toolName, isError, message)` — records
  failures for later attribution (ADR-16).
- `finalize()` — resolves pending errors, sums per-message usage,
  builds the sorted `SessionStats`.

Two maps are deliberately merged into one each, for memory and GC
reasons on very large transcripts (a 500 MB file holds ~2M distinct
ids): `msgUsage`'s key set doubles as the "turn already counted" set,
and `seenIDs` maps a tool_use.id straight to its tool name.

### Line reading

`lineReader` (read.go) reads one line at a time from a `bufio.Reader`
with a 16 MiB per-line cap. It replaces `bufio.Scanner`, whose hard
token limit fails the whole file on one over-long line and cannot
resume after it. Lines are returned as `[]byte` aliasing the reader
buffer (no copy for the common single-fragment case), valid only until
the next call. Over-long lines are reported as `tooLong` and fully
consumed, so the caller can skip them and continue.

### Parallel parsing (files > 32 MiB)

`analyzeFileParallel` splits the file into byte-range chunks (one
goroutine per chunk, up to 8, `runtime.NumCPU()`). A chunk owns every
line that **starts** inside its range: a line straddling a boundary is
read fully by the chunk it starts in, and the next chunk skips its
remainder (unless the boundary is exactly a line boundary, detected by
reading the byte before the chunk start).

Dedupe that must be global across chunks — tool_use.id, message.id,
line uuid — lives in `sharedDedupe`, guarded by one mutex. The mutex
is only held for the map lookup/insert, never for JSON parsing or
counting, so workers stay parallel. Error attribution is deferred to
finalize time (ADR-16), when the id→name map is complete for the whole
file; resolving eagerly would misattribute a `tool_result` whose
`tool_use` sits in another chunk that has not been parsed yet.

The merge is order-stable: per-message usage keeps the value from the
highest byte offset; every other counter simply adds up. Parallel
output is byte-identical to serial output, enforced by
`TestParallelMatchesSerial` (which lowers the threshold so small
files take the parallel path) and by the 426.5 MiB run, whose
single-file and directory outputs were identical.

Files ≤ 32 MiB take the serial path: simpler, less overhead, same
result. Pi files are always serial (their tree structure is not
line-independent).

### Pi reader

`parsePiFile` (pi.go) reads the whole file's entries (two passes over
in-memory entries: resolve the active branch, then aggregate). Entries
are small and the file is bounded by the 500 MiB input limit, so
holding them is acceptable; the Claude path stays streaming.

## Testing strategy

| Layer | How | What it proves |
|---|---|---|
| Unit tests (`internal/transcript`, 17 tests) | `go test ./...` | Each counting rule in isolation: dedupe, chunked messages, uuid duplicates, string content, JSON-string input, server_tool_use, over-long lines, pi active branch, parallel==serial, input-limit on both paths |
| Fixtures (`--self-test`, 6 checks) | `agentstats --self-test` | Whole-file output matches hand-computed `expected.json` exactly, for `basic`, `malformed`, `two`, `chunked`, `pi` and `compare:two`. Fixtures are embedded, so it works from a built binary anywhere |
| CLI tests (`cmd/agentstats`, 17 tests) | `go test ./cmd/agentstats` | The real built binary as a subprocess: exit codes (0/1/2/3), stdout JSON, `--out` file, `--request` envelope, stderr telemetry shape. Added in 0.1.1 because v0.1.0 had 0% coverage here |
| Race detector | `go test -race ./...` | The parallel parser's shared state is race-free |
| Benchmark | `go test -bench .` (`BenchmarkSession50MB`) | 52 MB / 170k turns; guards the 60 s limit at scale |
| Real scale | manual, `session` on a generated 426.5 MiB / 1.4M-turn file (single file and directory) | 60 s limit and exact counts near the 500 MiB boundary (~22 s); a 544 MB file is refused with `INPUT_TOO_LARGE` on both paths |
| ARM64 | `make test-arm64` (qemu-aarch64-static) | The arm64 binary actually executes: version, doctor, self-test |
| Release | `make release` | Static amd64/arm64 binaries + tarballs + `SHA256SUMS` verified with `sha256sum -c` |

Note on coverage: `go test -cover` reports `cmd/agentstats` at 0.0%
even though 17 CLI tests exist. That is by design: the CLI tests run
the built binary in a separate process, and Go coverage only counts
code executed inside the test binary. CLI correctness is measured by
the 17 tests, not by that percentage. `internal/transcript` coverage
is 74.1%.

## Release process

`make release` builds static linux-amd64 and linux-arm64 binaries
(`CGO_ENABLED=0`, `-trimpath`), tarballs them with `plugin.toml`, and
writes `SHA256SUMS`. Binaries are **not** committed to git. There is
no tag or GitHub Release created by this project: the GitHub App has
no create-tag / create-release operation, and the tag is created from
a checkout with `git tag v0.1.1 && git push origin v0.1.1` when wanted
(see `docs/CI.md` for the CI situation, which is the same class of
limitation).

## Invariants a future change must keep

1. Stdout carries exactly one JSON object per run (success or
   failure envelope). Never print usage text to stdout before an
   error envelope.
2. Output is deterministic and parallel output is byte-identical to
   serial output. Any new counting rule must work in both paths, or
   be covered by `TestParallelMatchesSerial`.
3. A malformed line, an over-long line, or an unknown line type never
   fails a run; it is skipped and counted.
4. No network, no model calls, no writes outside `--out`.
5. The 500 MiB and 60 s limits stay enforced and stay documented in
   `plugin.toml` and `docs/DESIGN.md`.
