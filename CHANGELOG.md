# Changelog

All notable changes to agentstats are documented here. The format follows
Keep a Changelog, and the project adheres to Semantic Versioning.

## [0.1.1] - 2026-11-08

### Fixed

- Turns and tokens on real Claude Code transcripts: one API message is
  several `assistant` lines sharing `message.id` and repeating the same
  `usage`, so v0.1.0 overcounted turns and tokens 2–3×. Now one turn
  per distinct `message.id`, tokens from the last line's usage.
- Compaction duplicates: lines re-appended after `/compact` (same
  `uuid`) are now counted once.
- Plain-text `message.content` (a string, not an array) no longer
  fails to parse and inflate `skipped`.
- `tool_use.input` given as a JSON-encoded string is now read.
- One line longer than 16 MiB no longer fails the whole file with
  `READ_ERROR`; it is skipped and counted, and reading continues.
- Running with no arguments no longer prints usage text before the
  error envelope (stdout stays one parseable JSON object).

### Added

- Real pi (format v3) reader: `session` header detection, `toolCall` /
  `toolResult` / `bashExecution` messages, `usage` entries, compaction
  and branch-summary usage, active-branch-only counting, pi tool names
  normalised to Claude-style names (`bash` → `Bash`, …).
- `server_tool_use` blocks counted as calls.
- Parallel parsing for files over 32 MiB (byte-range chunks, global
  dedupe); output is byte-identical to serial parsing, enforced by
  `TestParallelMatchesSerial` and by a 544 MB run.
- 17 CLI tests (`cmd/agentstats/main_test.go`) running the real binary
  as a subprocess: exit codes, stdout JSON, `--out`, `--request`
  envelope, telemetry.
- `BenchmarkSession50MB` and `make test-arm64` (runs the arm64 binary
  under qemu-aarch64-static).
- `docs/DESIGN.md`, `docs/ARCHITECTURE.md`, `docs/DECISIONS.md`
  (30 ADRs), `docs/CI.md` + `docs/ci.yml` (the CI workflow copy — the
  GitHub App cannot push `.github/workflows/`, 403).

### Changed

- 544 MB / 1,800,000-turn transcript: ~41 s wall (was ~78 s serial),
  under the 60 s limit; the 60 s deadline is now also checked while
  reading a single file (`TIMEOUT`, exit 3).
- `internal/transcript` coverage 69.1% → 74.1%; `cmd/agentstats`
  coverage stays 0.0% by design (subprocess tests — see
  `docs/ARCHITECTURE.md`).

## [0.1.0] - 2026-11-08

### Added

- `session` command: read a Claude Code or pi JSONL transcript directory
  (or a single `.jsonl` file, recursing at most two levels) and report
  turns, tokens, calls, failures, repeated Bash commands, top files and
  errors as one deterministic JSON object.
- Deduplication of `tool_use` by id, so `api-request-blob` duplicates of
  assistant calls are counted once; `result` rows do not double-count tokens.
- `compare` command: delta between two session outputs.
- `top` command: sessions sorted by calls, tokens or errors, with a total row.
- `self-test` command: runs every fixture in `fixtures/` against its exact
  expected JSON; exits 0 only when all match.
- `doctor` command: runtime checks; exits 0 only when the tool can run.
- `--version` / `version`, `--json`, `--request` arabica-plugin-v1 envelope.
- Telemetry: one JSON line to stderr per run.
- `plugin.toml` (arabica-plugin-v1), JSON schemas in `schema/`,
  fixtures, Makefile, and CI running tests, `--self-test` and `doctor`.
