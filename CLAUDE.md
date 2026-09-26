# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

`starawesome` is a Go CLI that reads an "awesome list" (markdown) and ranks the GitHub repos it links to by stars or last commit. Standard library only, single `package main` under `src/`. Flags and usage: see `README.md`.

## Commands

    go build -o starawesome ./src
    go test ./src/...
    go test ./src/... -run '^TestFetchGraphQL$'   # single test
    go vet ./src/...

## Architecture

`run()` in `src/main.go` wires a linear pipeline:

1. `readSource` (`input.go`): stdin, local file or URL. A `github.com/owner/repo` URL fetches that repo's README via the API and returns it as `self`, so the list does not rank itself.
2. `parseMarkdown` (`parse.go`): regex scan, not a full markdown parser. Collects GitHub repos per heading (`Entry.Category`), skips headings inside code fences. `Repo.Key()` is case-insensitive.
3. `fetchAll` (`fetch.go`): worker pool over batches. `chooseAPI` picks `fetchGraphQL` (one aliased query per batch, needs a token) or `fetchREST` (one repo per request, batch size 1).
4. `Client.do` (`client.go`): retries network errors, 5xx and rate limits, including GraphQL's HTTP 200 + `RATE_LIMITED` error.
5. `output.go`: `groupSections` keeps original heading order and drops failed or renamed duplicates; `rankSections` applies `-min-stars`, `-sort`, `-top`; then `writeTable` or `writeMarkdown`.

## Invariants

- A `fetchFunc` must return exactly one `Result` per input repo.
- The token is only sent when the request URL matches `Client.apiBase` (scheme + host). Never loosen this; `readSource` fetches user-supplied URLs.
- All untrusted text (descriptions, headings, links, error bodies) must pass through `oneLine` before printing, to block terminal escape injection.
- `LAST COMMIT` means the default branch's last commit with GraphQL, but `pushed_at` (any branch) with REST.

Tests live in `src/main_test.go` and use `httptest.Server` with `Client.apiBase` pointed at it (`apiServer` helper).
