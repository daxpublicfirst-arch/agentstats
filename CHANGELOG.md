# Changelog

All notable changes to agentstats are documented here. The format follows
Keep a Changelog, and the project adheres to Semantic Versioning.

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
