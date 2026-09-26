# starawesome

Ranks the GitHub repos of an awesome list by stars.

## Build

```sh
go build -o starawesome .
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
| `-headline`, `-h` | `false` | keep headlines, rank by stars within each |
| `-api` | `auto` | `auto`, `graphql` or `rest` |
| `-workers` | `8` | concurrent workers |
| `-retries` | `5` | max retries per request |
| `-batch` | `50` | repos per GraphQL request |
| `-v` | `false` | list every skipped non-GitHub link |

Use `-help` for help.

## Example

```sh
starawesome -headline https://github.com/andyrewlee/awesome-agent-orchestrators
```
