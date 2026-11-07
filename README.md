# agentstats

Read a Claude Code or pi JSONL transcript and report where the session
spent calls, tokens, time and errors, and which commands it repeated.

It is a standalone CLI. It knows nothing about any harness, makes no model
calls, uses no network, and only reads local `.jsonl` files. It writes
only under `--out` when given, otherwise it prints one JSON object to
stdout.

## Example

```sh
agentstats session ~/.claude/projects/-home-me-project --json
```

```json
{"ok":true,"tool":"agentstats","version":"0.1.0",
 "sessions":[
   {"id":"...","file":"/abs/session.jsonl","turns":42,
    "tokens":{"in":100,"out":200,"cache_read":3000,"cache_write":400},
    "calls":{"Bash":123,"Read":4,"Write":2},
    "failed":{"Bash":2},
    "repeated":[{"command":"git status --short","count":8}],
    "top_files":[{"path":"src/a.ts","count":4}],
    "errors":[{"tool":"Bash","message":"Exit code 1 ..."}]}
 ],
 "totals":{"sessions":1,"turns":42,"tokens":{"in":100,"out":200,"cache_read":3000,"cache_write":400},"calls":{"Bash":123,"Read":4,"Write":2},"failed":{"Bash":2}},
 "skipped":0}
```

## Commands

```text
agentstats session <dir> [--json] [--out <dir>]
agentstats session --session <dir> [--out <dir>]
agentstats compare <a.json> <b.json> [--json]
agentstats top <dir> --by calls|tokens|errors [--limit <n>] [--json]
agentstats self-test
agentstats doctor
agentstats version
agentstats --request <path>   # arabica-plugin-v1 envelope instead of flags
```

`<dir>` is a project directory containing `.jsonl` files, a single
`.jsonl` file, or a directory whose subdirectories contain `.jsonl`
files. It recurses at most two levels and skips hidden directories.

Every command supports `--json` and writes one JSON object to stdout.
One telemetry line is written to stderr per run:

```json
{"tool":"agentstats","version":"0.1.0","command":"session",
 "duration_ms":123,"input_bytes":4567,"output_bytes":890,"ok":true}
```

Exit codes: `0` success, `1` no transcript found (an empty answer, not
an error), `2` usage or input error, `3` tool failure. On failure one
JSON object is printed and the exit code is non-zero:

```json
{"ok":false,"error":{"code":"NO_TRANSCRIPT","blocked":"...","fix":"...","if_that_fails":"..."}}
```

## What is counted

- `turns` counts assistant messages, not tool calls.
- `tokens` sums the `usage` row once per assistant message.
  `api-request-blob` rows and `result` rows repeat that usage, so they
  are not counted again.
- `calls` counts unique `tool_use` blocks by name, deduplicated by
  `tool_use.id`. An `api-request-blob` duplicates a call that also
  appears in an `assistant` event; each unique call is counted once.
  Blocks without an id fall back to a name+input key.
- `failed` counts unique `tool_result` blocks with `is_error: true`,
  by the tool that produced them (looked up from the `tool_use.id`).
- `repeated` lists Bash commands seen more than once after
  normalisation: collapse whitespace, replace absolute paths under the
  session's project root with `<root>`, replace long hex ids with
  `<id>`, and drop arguments after `--` only if none of them looks
  like a file path. Top 20, by count desc then command.
- `top_files` counts `Read` (`file_path`/`path`), `Grep` (`path` only —
  its `pattern` is a search regex, not a file) and `Glob` (`pattern`)
  inputs by path. Top 20.
- `errors` keeps the first 200 characters of each distinct failed
  result. Top 20 by count.
- Lines that do not parse are skipped and counted in `skipped`;
  they are not fatal. Empty lines are ignored.
- Output is deterministic: maps are sorted by key, lists by count
  desc then name.

`compare <a.json> <b.json>` reads two `session` outputs and prints the
delta `b - a` for turns, tokens, calls and failed calls; map entries
and token categories with a zero delta are omitted.

`top <dir> --by calls|tokens|errors` prints the sessions sorted by
that measure (tokens = in + out + cache_read + cache_write), with a
`total` row. Default limit is 20.

`--self-test` runs every fixture in `fixtures/` against its exact
expected JSON and exits 0 only when every one matches. Fixtures are
embedded in the binary, so it works from a built binary anywhere.
`doctor` checks the runtime (OS/arch, writable temp dir, embedded
fixtures) and exits 0 only when the tool can run.

`--request <path>` reads an arabica-plugin-v1 envelope instead of flags:

```json
{"protocol":"arabica-plugin-v1","command":"session",
 "args":{"session":"/abs/project","out":"/abs/out"}}
```

## Setup and tests

Requires Go 1.25+. The module uses only the standard library, so the
build needs no network.

```sh
go vet ./...
go test ./...
go build -o bin/agentstats ./cmd/agentstats
bin/agentstats self-test   # must exit 0
bin/agentstats doctor      # must exit 0
make check                 # fmt + vet + test + self-test + doctor
make release               # static linux-amd64/arm64 binaries, tarballs, SHA256SUMS in dist/
```

## Layout

```text
agentstats/
  go.mod
  cmd/agentstats/main.go
  internal/transcript/{read.go,count.go,repeat.go,compare.go,selftest.go,doctor.go}
  fixtures/{basic,malformed,two}/   # .jsonl inputs + expected.json outputs
  fixtures/fixtures.go              # embeds the fixtures into the binary
  schema/{request,response,error,telemetry}.json
  plugin.toml
  README.md
  LICENSE
  CHANGELOG.md
  Makefile
```

## Corrections to the original task draft

The task prompt was a draft; the following were fixed during research
instead of followed literally:

- The prefix template named `logreduce` in its verification and commit
  steps. The target repo for this tool is `daxpublicfirst-arch/agentstats`
  (per §0 of the prefix); all work here is on `agentstats` only.
- The prompt said both "fail loudly" and "exit 1, no transcript found
  (not an error, an empty answer)". Both are kept: a missing/unreadable
  path is a `NO_TRANSCRIPT` error (exit 2); an existing directory with
  no `.jsonl` files is the empty `ok:true` answer (exit 1).
- `plugin.toml` had `source = "https://<your-repo>"`; it now points at
  the real repo, `https://github.com/daxpublicfirst-arch/agentstats`.
- The `--` rule ("drop arguments after `--` only if they are not file
  paths") was ambiguous; it is implemented as: drop the tail after
  `--` unless any of those arguments contains `/` or starts with
  `.`/`~`.
- `result` rows carry a `usage` object that duplicates the assistant
  rows; counting it would double-count tokens, so it is ignored.
- `top`'s output shape was unspecified; it returns
  `{ok, by, limit, sessions:[{id,file,turns,tokens,calls,errors}], total, ...}`.
- The 60 s limit is enforced as a deadline while reading; the 500 MiB
  input limit is enforced before reading.
- The GitHub MCP tools available to this session expose no create-tag
  or create-release operation, so the `v0.1.0` tag and the GitHub
  release cannot be created through them; the release tarballs are
  produced by `make release` instead. The tag can be created from a
  checkout with `git tag v0.1.0 && git push origin v0.1.0`.
- The Muse GitHub App cannot write `.github/workflows/ci.yml` (403 —
  the app lacks the `workflows` permission), so the CI file lives in
  this checkout but is not committed by the app; add it from your own
  checkout or the web UI. The workflow itself only runs
  `go vet`, `go test`, `self-test` and `doctor`.

## Limitations

- Only the JSONL shapes documented above are read; unknown line types
  with a `message.content` array are read generically, other lines are
  skipped and counted.
- A `tool_result` whose `tool_use_id` was never seen is counted under
  the tool name `unknown`.
- Session ids come from the `sessionId` field when present, otherwise
  from the file name.
