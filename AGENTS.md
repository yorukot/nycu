# nycu

A remote MCP server in Go (`cmd/nycu`, `internal/`) for NYCU E3 (Moodle) and the public timetable, protected by a built-in OAuth 2.1 server and deployed on Dokploy. Read [README.md](README.md) first.

## Layout

- `cmd/nycu`: the binary, with the `serve`, `e3-token` and `healthcheck` subcommands.
- `internal/e3`: the Moodle Web Services client and one method per E3 tool.
- `internal/timetable`: the timetable client and the `cos_time` parser.
- `internal/mcpserver`: tool registration and the shared result envelope.
- `internal/oauth`: the authorization server.
- `internal/truncate`: output-size limits.
- `internal/config`: environment parsing.

## Rules

- Every tool is read-only. A tool that writes would need `readOnlyHint: false`, a `confirm` parameter and, where the upstream system supports it, `dry_run`.
- Never pass a password or a TOTP secret through an MCP tool. The E3 password is entered only at the local `nycu e3-token` prompt.
- Never put a token, a token URL or a password in tool output, errors or logs.
  - `net/http` errors (`*url.Error`) contain the request URL, and a download URL carries the token. Convert such errors before returning them; see `networkReason` in `internal/e3/client.go`.
  - The access log records the path only, never the query string.
- Keep tool names, parameters and output shapes stable. A change to any of them is a breaking change. Adding an output field is not.
- Upstream JSON is loosely typed. Read it through `internal/jsonx`, and pass upstream values through as they are instead of converting them.
- Report every tool failure as a `*toolerr.Error` with message, why and hint.
- Repository documents and code comments are English.

## Verify

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

- Tests use synthetic servers (`httptest`). `NYCU_LIVE=1` adds calls to the real timetable and E3. With `E3_TOKEN` set, it also runs read-only E3 calls.
- `docker build .` must succeed; CI runs it.

## Release

1. Merge to `main`. CI must pass.
2. Dokploy builds the `Dockerfile` from `main` and redeploys. Pass `VERSION` as a build argument to stamp the version.
