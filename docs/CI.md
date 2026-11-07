# CI — why the workflow is not on `main` yet

## The situation

The workflow file `.github/workflows/ci.yml` exists in this checkout,
but it is **not** on `main`, and the Muse GitHub App cannot put it
there: pushing any file under `.github/workflows/` fails with

```text
403 Resource not accessible by integration
```

because the app lacks the `workflows` permission. This was verified
directly (2026-11-08, push attempt of exactly that file). The same
limit also blocks creating tags and GitHub Releases through the app —
see ADR-24 and ADR-25 in `docs/DECISIONS.md`.

## What to do (one step, from your own checkout)

The workflow is copied here as `docs/ci.yml`, byte-identical to
`.github/workflows/ci.yml` in this checkout. From a clone where you
can push normally:

```sh
git clone https://github.com/daxpublicfirst-arch/agentstats.git
cd agentstats
mkdir -p .github/workflows
cp docs/ci.yml .github/workflows/ci.yml
git add .github/workflows/ci.yml
git commit -m "Add GitHub Actions CI workflow"
git push origin main
```

That is all. After that, every push to `main` runs the workflow.

## What the workflow does

On `ubuntu-latest` with Go 1.25:

```sh
go vet ./...
go test ./...
go build -o /tmp/agentstats ./cmd/agentstats
/tmp/agentstats self-test
/tmp/agentstats doctor
```

It does not build releases and does not publish anything.

## If the copy drifts

`docs/ci.yml` and `.github/workflows/ci.yml` must stay identical.
If you edit one, copy it over the other and commit both. A future
`make check` step may enforce this automatically; it does not yet.
