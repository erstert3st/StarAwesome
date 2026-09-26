# starawesome

Ranks the GitHub repos of an awesome list by stars.

## Build

```sh
go build -o starawesome ./src
```

## Usage

```sh
starawesome [flags] [SOURCE]
```

`SOURCE`: GitHub repo URL, raw markdown URL, local file, or `-`/empty for stdin.
Token: `$GITHUB_TOKEN`, `$GH_TOKEN` or `gh auth token`.

| Flag | Default | Description |
|---|---|---|
| `-format` | `table` | `table` or `md` |
| `-headline` | `false` | keep headlines, rank by stars within each |
| `-api` | `auto` | `auto`, `graphql` or `rest` |
| `-workers` | `8` | concurrent workers |
| `-retries` | `5` | max retries per request |
| `-batch` | `50` | repos per GraphQL request |
| `-v` | `false` | list every skipped non-GitHub link |
| `-sort` | `stars` | `stars` or `commit` (newest last commit first) |
| `-min-stars` | `0` | hide repos with fewer stars |
| `-top` | `0` | show only the first N repos (per headline with `-headline`/md), `0` = all |
| `-stale` | | mark repos without a commit for this long: `90d`, `6w`, `18m`, `2y` |
| `-version` | `false` | print version and exit |
| `-completion` | | print shell completion script and exit: `zsh` |

Use `-h` or `-help` for help.

`LAST COMMIT` is the latest commit on the default branch (GraphQL). With
`-api rest` it is the last push to any branch, which saves one request per repo.

## Shell completion (zsh)

The script is generated from the binary's flag definitions, so descriptions
always match `-help`. Add to `~/.zshrc` (after `compinit`):

```sh
source <(starawesome -completion zsh)
```

## Example

```sh
starawesome -headline https://github.com/andyrewlee/awesome-agent-orchestrators

# 20 most recently active repos with at least 100 stars, stale ones marked
starawesome -sort commit -min-stars 100 -top 20 -stale 1y https://github.com/andyrewlee/awesome-agent-orchestrators
```
