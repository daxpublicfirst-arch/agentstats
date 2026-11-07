# agentstats — Design

`agentstats` reads Claude Code or pi JSONL transcripts and reports where
a session spent calls, tokens, time and errors, and which commands it
repeated. It is a standalone CLI: no harness, no model calls, no
network, only local `.jsonl` files. One JSON object goes to stdout per
run; one telemetry JSON line goes to stderr.

This document describes *what* the tool does and *why* it does it that
way. The decisions behind each rule live in `DECISIONS.md` (one ADR per
decision, with alternatives and sources). The code layout and data flow
live in `ARCHITECTURE.md`.

## The two transcript shapes

### Claude Code

Claude Code writes one JSON object per line under
`~/.claude/projects/<slug>/<uuid>.jsonl`, append-only. Line types
include `user`, `assistant`, `system`, `summary`,
`file-history-snapshot`, `file-history-delta`, `queue-operation`,
`last-prompt`, `ai-title`, `attachment`, `api-request-blob` and
`result`. Only `assistant` and `user` lines carry countable stats;
the rest are ignored (never counted in `skipped`).

Three properties of real Claude Code files drive the parser, and all
three were verified against the format references in `DECISIONS.md`
(ADR-04, ADR-05) rather than assumed from the original draft:

1. **One API message is several `assistant` lines.** Claude Code
   writes one line per content block (thinking, text, each
   `tool_use`), all sharing the same `message.id`, and every line
   repeats the whole message's `usage`. Summing usage over lines
   overcounts tokens and cost 2–3×. So a *turn* is one distinct
   `message.id`, and tokens are summed once per distinct `message.id`,
   taking the **last** line's usage (in background-agent transcripts
   the usage grows block by block and only the final line has the full
   figures). Lines without `message.id` (older files) count one turn
   each, as before.
2. **After `/compact`, history is re-appended.** Each compaction
   re-appends the earlier `user`/`assistant` lines as exact copies
   with the same top-level `uuid`, so a long session can hold every
   event three or four times. Lines are deduplicated by `uuid`: the
   first line per `uuid` counts, later copies are ignored silently
   (not counted in `skipped` — they are exact duplicates, not errors).
3. **`message.content` is a string or an array.** User prompts are
   usually a plain string; tool results are always an array. A string
   carries no blocks and is never an error.

`tool_use` blocks are deduplicated by `tool_use.id`: an
`api-request-blob` repeats a call that also appears in an `assistant`
event, so each unique call is counted once. Blocks without an id fall
back to a name+input key. `server_tool_use` blocks (built-in server
tools such as web search) are counted the same way. `tool_use.input`
may be a JSON object (local transcripts) or a JSON-encoded string
(the shape the Compliance API returns); both are read.

`result` rows repeat the usage of the assistant messages that
produced them, so their usage is ignored — except that a `result`
row's `usage` is the *only* usage in `--print`-only transcripts, which
then report 0 tokens. That is a known limitation (ADR-06).

### Pi (format v3)

Pi writes one JSONL file per session at
`~/.pi/agent/sessions/--<cwd>--/<timestamp>_<uuid>.jsonl`. The first
line is a `session` header (`{"type":"session","version":3,"id",...}`).
Every other entry extends `{type, id, parentId, timestamp}` and the
entries form a **tree**: resuming a branch appends in place instead of
starting a new file.

Entry types that carry stats:

- `message` with `message.role == "assistant"`: one turn; `content`
  blocks are `text` / `thinking` / `toolCall{id, name, arguments}`.
  Its `usage` (`{input, output, cacheRead, cacheWrite, ...}`) is
  counted once per message — pi assistant messages are complete, not
  per-block chunks.
- `message` with `role == "toolResult"`: `toolCallId`, `toolName`,
  `content`, `isError`. A failed result counts under its tool; its
  nested `usage` (model work done by the tool itself) is also counted.
- `message` with `role == "bashExecution"`: the TUI `!` command
  (`{command, output, exitCode}`); one Bash call, failed when
  `exitCode` is non-zero.
- `usage` entries (`{kind, usage}`): model-attributed usage outside
  any assistant message (e.g. cache warming); counted.
- `compaction` and `branch_summary` entries: their `usage` (spent
  generating the summary) is counted. `tokensBefore` is a point-in-time
  figure, not an amount spent, and is never added.

Only the **active branch** is counted: entries are walked from the
last entry in the file back to the root via `parentId`, and entries off
that path (abandoned branches) are ignored — counting every branch
would count the same work twice. Compaction does not delete entries in
pi, so pre-compaction entries on the active branch are counted: they
are work the session actually did, even though the model no longer
sees them.

Pi tool names are lowercase (`bash`, `read`, `write`, `edit`, `grep`,
`glob`); they are normalised to the capitalised names used in
`calls` (`Bash`, `Read`, …) so one session's Bash calls are counted
under `Bash` whichever agent drove it. Unknown names pass through
unchanged. Pi argument keys are read generically (`command`,
`path`, `file_path`, `filePath`, `pattern`), because `arguments` is an
open record.

A pi file is detected by its `session` header line. A pi file without
its header line is treated as Claude-shaped and its pi-only entries
are ignored (documented, not silently guessed).

## What is counted

- `turns`: assistant messages (Claude: distinct `message.id`; pi: one
  per assistant message entry on the active branch).
- `tokens`: `usage` summed once per assistant message. Cache fields
  map to `cache_read` / `cache_write`.
- `calls`: unique tool calls by name (see dedupe above).
- `failed`: `tool_result` / pi `toolResult` blocks with `is_error`
  true, by the tool that produced them (looked up from the
  `tool_use.id`, or `unknown` when that id was never seen).
- `repeated`: Bash commands seen more than once after normalisation
  (below). Top 20, by count desc then command.
- `top_files`: `Read` (`file_path`/`path`), `Grep` (`path` only — its
  `pattern` is a search regex, not a file) and `Glob` (`pattern`)
  inputs, by path. Top 20.
- `errors`: first 200 characters of each distinct failed result.
  Top 20 by count.
- `skipped`: lines that did not parse, lines with neither a `type`
  nor a `message`, and lines longer than 16 MiB (below). Empty lines
  are ignored, not counted. Compaction duplicates are ignored, not
  counted.

`compare <a.json> <b.json>` reads two `session` outputs and prints
the delta `b - a` for turns, tokens, calls and failed calls; map
entries and token categories with a zero delta are omitted.

`top <dir> --by calls|tokens|errors` prints the sessions sorted by
that measure (tokens = in + out + cache_read + cache_write), with a
`total` row. Default limit is 20.

## Normalisation of repeated Bash commands

1. Collapse runs of whitespace to single spaces, trim ends.
2. Replace absolute paths under the session's project root with
   `<root>`.
3. Replace long hex ids with `<id>`: 32+ hex characters anywhere, and
   whole tokens of 8+ hex characters that contain both a letter and a
   digit.
4. Drop arguments after `--` only if none of them looks like a file
   path (contains `/` or starts with `.`/`~`).

## Input discovery

`<dir>` is a project directory containing `.jsonl` files, a single
`.jsonl` file, or a directory whose subdirectories contain `.jsonl`
files. It recurses at most two levels and skips hidden directories.
Two levels also covers Claude Code subagent transcripts, which live at
`<session-id>/subagents/*.jsonl` (their `usage` never appears in the
main transcript, so counting them as separate sessions is correct).

Session ids come from the `sessionId` field (Claude) or the header
`id` (pi) when present, otherwise from the file name.

## Limits and failure behaviour

- **500 MiB input limit**: enforced before reading, from the summed
  file sizes. Over it: `INPUT_TOO_LARGE`, exit 2.
- **60 s deadline**: enforced while reading (checked every 4096
  lines), not just between files. Over it: `TIMEOUT`, exit 3. A run
  that is killed by the harness at 60 s would lose everything; the
  structured `TIMEOUT` envelope is the honest alternative.
- **16 MiB per line**: a line longer than this is skipped and counted
  in `skipped`, never fatal to the file, never loaded whole into
  memory. Real transcript lines are far smaller (Claude Code truncates
  tool-result text to ~10 KiB by default, ~1 MiB at most).
- **Exit codes**: `0` success, `1` no transcript found (an empty
  answer, not an error), `2` usage or input error, `3` tool failure.
  On failure exactly one JSON object (the error envelope) is printed
  to stdout, so stdout stays machine-parseable.
- **Determinism**: maps marshal with sorted keys; lists sort by count
  desc then name; parallel parsing produces byte-identical output to
  serial parsing (tested).

## Performance

Measured on this project's own test hardware (4-CPU cloud VM, shared
with other tenants — numbers move with load, treat them as orders of
magnitude):

| Input | Result |
|---|---|
| 11 MB / 40,000 turns | 0.87 s |
| 52 MB / 170,000 turns | ~2.8 s (benchmark, parallel path) |
| 544 MB / 1,800,000 turns | ~41 s wall under load (was ~78 s serial), under the 60 s limit; output byte-identical to the serial run |

Files over 32 MiB are parsed in parallel (one goroutine per
byte-range chunk, up to 8); see `ARCHITECTURE.md` for how the merge
stays exact. `go test -bench .` in `internal/transcript` reproduces
the 52 MB benchmark (`BenchmarkSession50MB`).
