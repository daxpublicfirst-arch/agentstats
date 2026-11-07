# agentstats — Decisions (ADRs)

One ADR per decision that a future reader (human or local agent) would
otherwise have to guess at or ask the owner about. Each ADR states the
context, the decision, the alternatives considered, and the trade-off.
Sources are linked where the decision rests on external facts.

## ADR-01: Go, standard library only, no network, no model calls

- **Context:** The tool reads local transcripts and prints stats. It
  must run inside a plugin sandbox with no network and no credentials.
- **Decision:** Go 1.25+, standard library only, `GOPROXY=off` builds.
- **Alternatives:** Python (simpler text handling, slower at 500 MB,
  needs a runtime on the host); Rust (faster JSON, heavier toolchain).
- **Trade-off:** Go's `encoding/json` is the main cost at 500 MB
  (see ADR-15); accepted, because a single static binary with no
  runtime dependency matters more for a plugin.

## ADR-02: One JSON object on stdout, telemetry line on stderr

- **Context:** The Arabica adapter parses stdout as JSON.
- **Decision:** Every run prints exactly one JSON object to stdout
  (success output or failure envelope) and exactly one telemetry JSON
  line to stderr.
- **Alternatives:** Human-readable stdout with `--json` as an option.
- **Trade-off:** Humans reading raw stdout see JSON; accepted, the
  adapter is the primary consumer and `--help`/`doctor` cover humans.

## ADR-03: Exit codes 0 / 1 / 2 / 3

- **Context:** The draft said both "fail loudly" and "exit 1, no
  transcript found (not an error, an empty answer)".
- **Decision:** 0 success; 1 existing path with no `.jsonl` files
  (empty `ok:true` answer); 2 usage/input error; 3 tool failure.
  A missing/unreadable path is `NO_TRANSCRIPT`, exit 2.
- **Alternatives:** Exit 1 for a missing path too.
- **Trade-off:** Callers can distinguish "nothing to analyse" (1)
  from "you gave me a bad path" (2).

## ADR-04: A turn is one distinct assistant `message.id`; usage counted once, last wins

- **Context:** Research showed one Claude Code API message is several
  `assistant` lines (one per content block) sharing `message.id`, every
  line repeating the whole message's `usage`. Summing usage over lines
  overcounts tokens and cost 2–3×.
- **Decision:** Count one turn per distinct `message.id`; keep the
  **last** line's usage for that id (in background-agent transcripts
  the usage grows block by block and only the final line has the full
  figures). Lines without an id count one turn each.
- **Alternatives:** First usage wins (wrong for background agents);
  sum all lines (2–3× overcount — the v0.1.0 behaviour on real files).
- **Trade-off:** This changed v0.1.0 behaviour on real transcripts
  (fixtures without `message.id` are unaffected). It is a correctness
  fix, not a preference.
- **Sources:** `pub-claude-code-cct` reference, "One API message =
  several `assistant` lines" —
  https://github.com/ibitlab/pub-claude-code-cct/blob/HEAD/docs/reference.md ;
  `claude-print` internals —
  https://github.com/jedarden/claude-print/blob/HEAD/docs/research/claude-code-internals.md

## ADR-05: Compaction duplicates deduplicated by line `uuid`, first wins

- **Context:** After `/compact`, Claude Code re-appends earlier lines
  as exact copies with the same top-level `uuid`; a long session can
  hold every event three or four times.
- **Decision:** First line per `uuid` counts; later copies are ignored
  silently (not counted in `skipped`).
- **Alternatives:** Count every line (triples counts after compactions);
  count them in `skipped` (misleading — they are valid lines).
- **Trade-off:** A `uuid` is trusted as a stable line identity; lines
  without one are never deduped this way.
- **Source:** same `pub-claude-code-cct` reference as ADR-04
  (`dedup_events`, "keep the first line per `uuid`").

## ADR-06: `result` rows are ignored for tokens

- **Context:** `result` rows carry a `usage` object that duplicates
  the assistant rows.
- **Decision:** Ignore `result` usage entirely.
- **Alternatives:** Count it when no assistant usage exists (would
  fix `--print`-only transcripts, which currently report 0 tokens).
- **Trade-off:** Kept simple and predictable; the `--print`-only gap
  is documented in `docs/DESIGN.md` rather than papered over with a
  heuristic that would be wrong for mixed files.

## ADR-07: `tool_use` deduplicated by id; no-id fallback is name+input

- **Context:** `api-request-blob` repeats calls that also appear in
  `assistant` events.
- **Decision:** Unique calls counted once by `tool_use.id`; blocks
  without an id fall back to a name+input key.
- **Alternatives:** Count every block (double-counts blobs).
- **Trade-off:** Two different calls with the same id (should not
  happen) would be undercounted; accepted, ids are unique per call.

## ADR-08: `server_tool_use` blocks counted as calls

- **Context:** Anthropic's API has a `server_tool_use` block type for
  built-in server tools (e.g. web search), same id/name/input shape
  as `tool_use`. The Compliance API normalises most server tool calls
  into `tool_use`/`tool_result`, so these blocks are rare locally.
- **Decision:** Count them exactly like `tool_use`.
- **Alternatives:** Ignore them (would silently drop web-search calls
  if they ever appear raw).
- **Trade-off:** If a transcript ever carried both a `server_tool_use`
  and a normalised `tool_use` for the same call with different ids,
  it would double-count; no evidence that happens.
- **Source:** Claude Compliance API docs (server tool normalisation),
  https://github.com/seangsisg/claude-code-docs/blob/HEAD/docs/docs__en__manage-claude__compliance-sessions.md

## ADR-09: `message.content` may be a string or an array

- **Context:** Research confirmed `message.content` is a string for
  plain user prompts and an array of blocks otherwise. v0.1.0
  declared it as an array, so every plain user message failed to
  parse and was counted in `skipped`.
- **Decision:** Custom unmarshaller accepting both; a string carries
  no blocks.
- **Alternatives:** Keep array-only (silently inflates `skipped` on
  every real transcript).
- **Trade-off:** None material.
- **Source:** `pub-claude-code-cct` reference ("either a string or an
  array of … blocks"); `unleash` CLI format doc.

## ADR-10: `tool_use.input` may be an object or a JSON-encoded string

- **Context:** Local transcripts carry `input` as an object; the
  Compliance API returns it as a JSON-encoded string.
- **Decision:** Parse the object directly; if it is a string, parse
  the string's contents as JSON.
- **Alternatives:** Ignore string inputs (would lose the command/path
  for every Compliance-API-shaped call).
- **Trade-off:** A string input that is not valid JSON yields no
  command/path, but the call itself is still counted.

## ADR-11: Pi v3 reader, active branch only

- **Context:** Pi's format is nothing like Claude Code's: a `session`
  header, then tree entries linked by `id`/`parentId`. The v0.1.0
  draft assumed pi "may use the same shape"; research showed it does
  not.
- **Decision:** A dedicated pi reader (`internal/transcript/pi.go`):
  detect by the `session` header, resolve the active branch by
  walking `parentId` from the last entry, count only entries on that
  path.
- **Alternatives:** Count every entry (double-counts abandoned
  branches); count only the model-visible projection (drops
  pre-compaction work that was actually done — the wrong question for
  a stats tool).
- **Trade-off:** Pi parsing is serial and holds entries in memory
  (bounded by the 500 MiB input limit); acceptable.
- **Sources:** Pi session format —
  https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/session-format.md ;
  Pi message types — https://pi.dev/docs/latest/message-types

## ADR-12: Pi tool names normalised to Claude-style names

- **Context:** Pi tools are lowercase (`bash`, `read`); Claude Code
  tools are capitalised (`Bash`, `Read`).
- **Decision:** Normalise the known set (`bash`→`Bash`,
  `read`→`Read`, `write`→`Write`, `edit`→`Edit`, `grep`→`Grep`,
  `glob`→`Glob`); unknown names pass through unchanged.
- **Alternatives:** Keep names as-is (a mixed directory would report
  `Bash` and `bash` as different tools, defeating cross-agent totals).
- **Trade-off:** Loses the literal pi name in output; the mapping is
  documented here and in `pi.go`.

## ADR-13: Pi `usage` entries and summary usage counted; `tokensBefore` never added

- **Context:** Pi docs say `usage` entries and the `usage` on
  `compaction`/`branch_summary` entries "contribute to session token
  and cost totals"; `tokensBefore` is the token count before
  compaction, a point-in-time figure.
- **Decision:** Add the `usage` objects; never add `tokensBefore`.
- **Alternatives:** Add `tokensBefore` (would add the whole
  pre-compaction history as if it were new spending).
- **Source:** Pi session format doc (same as ADR-11).

## ADR-14: Over-long lines are skipped, not fatal (16 MiB cap, `bufio.Reader`)

- **Context:** v0.1.0 used `bufio.Scanner` with a 16 MiB cap; one
  longer line failed the whole file with `READ_ERROR`, and the
  scanner cannot resume after `ErrTooLong`.
- **Decision:** Replace the scanner with a `bufio.Reader`-based
  `lineReader` that enforces the same 16 MiB cap itself: over-long
  lines are skipped, counted in `skipped`, and reading continues.
  The cap bounds memory; real lines are far smaller (tool-result text
  is truncated to ~10 KiB by default, ~1 MiB at most).
- **Alternatives:** Raise the cap (only moves the failure); use an
  unbounded reader (a hostile single-line file could exhaust memory).
- **Trade-off:** A single line over 16 MiB is lost rather than
  parsed; accepted, it cannot be a real transcript line.
- **Sources:** Go `bufio` docs (Scanner's documented limit, Reader
  recommended for large tokens); community write-ups on
  `bufio.Scanner: token too long`.

## ADR-15: Parallel parsing above 32 MiB, byte-identical to serial

- **Context:** Profiling (`go test -bench`, pprof) showed
  `encoding/json` dominates at 500 MB; the serial run took ~78 s
  wall under load, over the 60 s limit. Only parallelism (or a
  different JSON parser — rejected, stdlib-only, ADR-01) cuts it.
- **Decision:** Files over 32 MiB are split into byte-range chunks
  (one goroutine per chunk, up to 8). A chunk owns lines starting
  inside its range; dedupe state that must be global (tool_use.id,
  message.id, line uuid) lives in `sharedDedupe` behind one mutex
  held only for map lookup/insert. Merge is order-stable (per-message
  usage keeps the highest byte offset). Parallel output is
  byte-identical to serial output.
- **Alternatives:** More workers than CPUs (no gain under load);
  sharded maps without a mutex (complex, same result); raising the
  60 s limit (breaks the plugin contract).
- **Trade-off:** ~200 lines of concurrency code and a new class of
  bug (cross-chunk references). Mitigated by `TestParallelMatchesSerial`
  and by the 544 MB run whose parallel output was `cmp`-identical to
  the serial run. Files ≤ 32 MiB stay serial.

## ADR-16: Failed results are attributed at finalize time, not when seen

- **Context:** In parallel parsing, a `tool_result` in one chunk may
  refer to a `tool_use` in another chunk that has not been parsed yet.
  Resolving eagerly misattributed it to `unknown` (caught by
  `TestParallelMatchesSerial` on the `basic` fixture).
- **Decision:** Record failures as `{toolUseID, toolName, message}`
  and resolve the tool name in `finalize()`, when the id→name map is
  complete for the whole file. This also fixes the (theoretical)
  serial case of a result preceding its call.
- **Alternatives:** Two-pass parsing (parse twice the cost);
  resolving at merge time only for parallel (two code paths).
- **Trade-off:** One code path for both modes; `failed` is only
  final in `finalize()`, which is the only place it is read.

## ADR-17: Dedupe maps merged to bound memory

- **Context:** A 500 MB file holds ~2M distinct ids; one map per
  concern (seen ids, id→name, seen messages, per-message usage)
  costs hundreds of MB and GC time.
- **Decision:** `msgUsage`'s key set doubles as the "turn already
  counted" set; `seenIDs` maps id→name directly.
- **Alternatives:** Separate maps (simpler to read, heavier).
- **Trade-off:** Slightly less obvious code; commented at the struct.

## ADR-18: 60 s deadline checked while reading

- **Context:** v0.1.0 checked the deadline only between files, so a
  single huge file could run past 60 s and return late results.
- **Decision:** Check every 4096 lines inside each file's loop
  (serial, parallel chunks, pi); over the deadline, return the
  structured `TIMEOUT` error (exit 3).
- **Alternatives:** No in-file check (late results instead of an
  error); partial results on timeout (breaks the one-JSON-object
  contract and would be misleading).
- **Trade-off:** Under extreme load a max-size file can now fail
  with `TIMEOUT` where it previously succeeded slowly; that is the
  honest behaviour for a 60 s plugin limit.

## ADR-19: 500 MiB limit enforced before reading

- **Context:** Reading first and checking later wastes work and
  memory on oversized input.
- **Decision:** `DiscoverFiles` sums sizes and rejects over-limit
  input with `INPUT_TOO_LARGE` (exit 2) before any file is opened.
- **Trade-off:** None; the sum is cheap.

## ADR-20: Deterministic output

- **Context:** The adapter diffs outputs and tests compare them
  exactly.
- **Decision:** Maps marshal with sorted keys (Go `encoding/json`
  behaviour); lists sort by count desc then name; no timestamps or
  random order in output.
- **Trade-off:** None.

## ADR-21: CLI tests run the real binary as a subprocess

- **Context:** v0.1.0 had 0% coverage in `cmd/agentstats`: every CLI
  behaviour (flags, exit codes, envelope, `--out`) was only checked by
  hand.
- **Decision:** `cmd/agentstats/main_test.go` builds the binary once
  and runs it as a subprocess (17 tests). Go coverage still reports
  0.0% for that package — coverage only counts code executed inside
  the test binary — so CLI correctness is measured by the 17 tests,
  not by that percentage.
- **Alternatives:** Test `run()` in-process (would not cover process
  exit codes or the real stdout/stderr split).
- **Trade-off:** Tests are slower (~1–5 s, dominated by the build);
  accepted.

## ADR-22: No-args prints only the error envelope

- **Context:** The CLI tests caught that running with no arguments
  printed the usage text to stdout *before* the error envelope, so
  stdout was not parseable JSON.
- **Decision:** No-args prints only the envelope; the envelope's fix
  points at `--help`.
- **Alternatives:** Keep the usage print (breaks the one-JSON-object
  contract, ADR-02).
- **Trade-off:** Humans with no args see JSON instead of usage; the
  fix hint tells them where usage lives.

## ADR-23: Versioning — patch bump for behaviour fixes

- **Context:** 0.1.1 changes counting behaviour on real transcripts
  (ADR-04/05/09/11) and adds features (pi reader, parallel parsing).
- **Decision:** Semver patch bump to 0.1.1 (no breaking change to the
  output schema; the counts on real files are now *correct*, which is
  the point of the tool). `Version`, `plugin.toml`, `Makefile`,
  schemas, fixtures and docs move together.
- **Alternatives:** Minor bump to 0.2.0 (overstates the change for a
  first public version with no released tag yet).

## ADR-24: CI cannot be pushed by the GitHub App (403, `workflows` permission)

- **Context:** The Muse GitHub App cannot write
  `.github/workflows/ci.yml`: a direct push attempt fails with `403
  Resource not accessible by integration` (verified 2026-11-08 for
  this repo).
- **Decision:** Keep the workflow in the local checkout
  (`.github/workflows/ci.yml`) and a copy at `docs/ci.yml` with
  install instructions in `docs/CI.md`, so the owner (or a local
  agent with a normal git push) can add it in one step. Do not use a
  browser/web-UI step for it unless the owner asks.
- **Alternatives:** No CI at all (worse); a different CI host (more
  moving parts for the same checks).
- **Trade-off:** CI does not run on push until the owner adds the
  file once.

## ADR-25: No tag / GitHub Release created by the App

- **Context:** The GitHub tools available here expose no create-tag
  or create-release operation, and the owner asked not to use a
  browser/web-UI step for it.
- **Decision:** Releases are `make release` tarballs (local, not
  committed). A tag is created from a checkout with
  `git tag v0.1.1 && git push origin v0.1.1` when the owner wants one.
- **Trade-off:** No Release exists on GitHub until the owner creates
  it; accepted, it is out of scope by instruction.

## ADR-26: Bash normalisation rules

- **Context:** "Which commands did it repeat" is only useful if
  trivially different invocations of the same command collapse.
- **Decision:** Collapse whitespace; project-root paths → `<root>`;
  long hex ids → `<id>` (32+ hex anywhere; whole 8+ hex tokens with
  both a letter and a digit); drop args after `--` unless any looks
  path-like (`/`, leading `.`/`~`).
- **Alternatives:** No normalisation (every invocation distinct);
  full shell parsing (overkill and fragile).
- **Trade-off:** Normalisation is heuristic; two genuinely different
  commands can collapse (e.g. same command with different hex ids is
  *intended* to collapse).

## ADR-27: `top_files` counts paths, not patterns

- **Context:** `Grep`'s `pattern` is a search regex, not a file.
- **Decision:** `Read` counts `file_path`/`path`; `Grep` counts
  `path` only; `Glob` counts `pattern` (a glob *is* a path pattern).
- **Alternatives:** Count Grep's pattern too (would list regexes as
  "files").
- **Trade-off:** None material.

## ADR-28: Errors truncated to 200 chars, top-20 lists

- **Context:** A failed result can be megabytes of output; the
  response must stay bounded.
- **Decision:** Error messages truncated to 200 chars; `repeated`,
  `top_files` and `errors` capped at 20 entries each.
- **Alternatives:** No cap (unbounded output size).
- **Trade-off:** The 21st repeated command is invisible; accepted
  for a summary tool.

## ADR-29: Recursion at most two levels, hidden dirs skipped

- **Context:** A project directory may contain subdirectories with
  session files (and `.git`, `.claude`, …).
- **Decision:** Recurse at most two levels; skip hidden directories.
  Two levels also covers Claude Code subagent transcripts at
  `<session-id>/subagents/*.jsonl`.
- **Alternatives:** Unlimited recursion (could walk huge trees);
  one level (would miss subagent transcripts).
- **Trade-off:** A session nested three levels deep is ignored; the
  limit is documented in `docs/DESIGN.md`.

## ADR-30: ARM64 verified under QEMU, not on real hardware

- **Context:** The arm64 binary cannot run on the amd64 test host.
- **Decision:** `make test-arm64` runs the arm64 binary under
  `qemu-aarch64-static` (version, doctor, self-test). Skips
  gracefully when qemu is not installed.
- **Alternatives:** No arm64 execution check (only `file` output);
  real ARM hardware (unavailable here).
- **Trade-off:** QEMU proves the binary executes and behaves
  correctly; it does not prove native ARM performance.
