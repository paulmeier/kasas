<!--
PR titles must follow Conventional Commits (feat:, fix:, docs:, refactor:,
perf:, test:, ci:, chore:). The title becomes a CHANGELOG entry and drives the
next version, so write it for users. Add a `!` (e.g. feat!:) for breaking changes.
-->

## What & why

<!-- A short description of the change and the motivation. Link any issue. -->

## Checklist

- [ ] PR title follows Conventional Commits
- [ ] `make fmt` applied (gofmt) and `make lint` passes
- [ ] `make test` passes (and `make test-race` if touching concurrency)
- [ ] If queries or migrations changed, `make generate` was re-run and committed
- [ ] Docs updated (README / CONTRIBUTING / config.example.toml) if behaviour changed
