<div align="center">

<h1>NYCU</h1>

<p><strong>NYCU E3 and the course timetable as a remote MCP server for ChatGPT, Claude and other agents.</strong></p>

[![CI](https://img.shields.io/github/actions/workflow/status/yorukot/nycu/check.yml?branch=main&style=flat-square&label=CI)](https://github.com/yorukot/nycu/actions)
[![Go](https://img.shields.io/github/go-mod/go-version/yorukot/nycu?style=flat-square)](go.mod)
[![License](https://img.shields.io/github/license/yorukot/nycu?style=flat-square)](LICENSE)

**English** | [繁體中文](README.zh-TW.md)

</div>

```console
$ timetable_lookup_courses {"cos_ids": [516706, "535702"]}
{"data":[{"cos_id":"516706","name":"向量微積分","time":"R56-SA320[GF]",...},
         {"cos_id":"535702","name":"資料探勘","time":"T56-EC115[GF]",...}],
 "metadata":{"acysem":"1151","count":2,"missing":[]}}
```

NYCU coursework is spread over E3 (Moodle) and the public timetable. This server gives an agent 17 read-only tools for both. It is one Go binary with no database, and it is built to run on Dokploy and connect to ChatGPT (including Dots) over OAuth.

## Features

- **Reads E3**: courses, assignments, deadlines, announcements and replies, recent changes, notifications, quizzes, submissions, grades, course outlines, and file downloads (13 tools).
- **Searches the public timetable**: semesters, course search, lookup by cos_id, and the period table (4 tools).
- **Remote MCP**: Streamable HTTP at `/mcp`, which works with ChatGPT custom MCP servers, Claude Code and other MCP clients.
- **Built-in OAuth 2.1**: dynamic client registration, PKCE, and refresh tokens. One owner password approves each client, and nothing is stored.
- **Read only**: no tool writes to E3. Tokens never appear in tool output, errors or logs.

## Quick start

1. **Get an E3 token** on your own computer. The command prompts for the password without echoing it. Only E3 receives the password.

   ```sh
   go run github.com/yorukot/nycu/cmd/nycu@latest e3-token
   ```

   From a clone, `go run ./cmd/nycu e3-token` does the same. It prints `E3_TOKEN=…` and `E3_USERID=…`.
2. **Deploy on Dokploy.**
   1. Create an Application with GitHub as the source. Use `yorukot/nycu`, branch `main`, and build type **Dockerfile**.
   2. Add the environment variables from [Configuration](#configuration). Generate `AUTH_SECRET` with `openssl rand -base64 48`.
   3. Under **Domains**, add your domain with HTTPS on and container port `8080`. Set `PUBLIC_URL` to the same `https://` origin.
   4. Deploy. `https://<domain>/healthz` should return `ok`.
3. **Connect ChatGPT.**
   1. In ChatGPT, open **Plugins**, select **+**, then **Add custom MCP server**. Your plan may require developer mode for custom servers.
   2. Enter the URL `https://<domain>/mcp` and choose **OAuth**.
   3. ChatGPT opens the server's authorization page. Enter `OWNER_PASSWORD` there.
   4. A Dot can then use the plugin. Ask "Which E3 assignments are due this week?".
4. **Or connect Claude Code.** Run `claude mcp add --transport http nycu https://<domain>/mcp`, then run `/mcp` to sign in.

## Usage

| Service | Tools |
|---------|-------|
| E3 | `e3p_whoami`, `e3p_list_courses`, `e3p_list_assignments`, `e3p_list_due`, `e3p_list_announcements`, `e3p_get_discussion`, `e3p_list_updates`, `e3p_list_notifications`, `e3p_list_quizzes`, `e3p_get_submission`, `e3p_list_grades`, `e3p_get_content`, `e3p_download_file` |
| Timetable | `timetable_list_semesters`, `timetable_search_courses`, `timetable_lookup_courses`, `timetable_get_periods` |

- Every result has the same shape: `{"data": …, "metadata": {…}}` on success, and `{"error": {"message", "why", "hint"}}` with `isError` on failure.
- `cos_id` is the part after the dot in an E3 course shortname. `1151.535702` means acysem `1151` and cos_id `535702`, so `timetable_lookup_courses` chains off `e3p_list_courses`.
- `e3p_download_file` takes a `pluginfile.php` URL from `e3p_get_content`, `e3p_list_announcements` or `e3p_get_discussion`. It returns the file as an embedded resource: text files as text, other files as base64.
- When a string is too long, the result is shortened. The text then contains a `truncated` marker, and the result gets a `_truncation` field that names the fields and says how to narrow the call.

## Configuration

| Variable | Required | Default | Meaning |
|----------|----------|---------|---------|
| `PUBLIC_URL` | yes | | External origin, such as `https://nycu.example.com`. OAuth metadata uses it. |
| `OWNER_PASSWORD` | yes | | Password for the authorization page, at least 12 characters. |
| `AUTH_SECRET` | yes | | Key that signs client IDs, codes and tokens, at least 32 bytes. Changing it revokes every session. |
| `E3_TOKEN` | yes | | E3 Web Services token from `nycu e3-token`. |
| `E3_USERID` | no | looked up | Your E3 user ID. Setting it saves one call. |
| `E3_BASE` | no | `https://e3p.nycu.edu.tw` | Moodle site root. |
| `PORT` | no | `8080` | Listen port. |
| `E3_DOWNLOAD_MAX_BYTES` | no | `10485760` | Largest file `e3p_download_file` returns. |
| `MCP_MAX_STRING_CHARS` / `MCP_MAX_TOTAL_CHARS` | no | `20000` / `120000` | Truncation limits for one result. |

The E3 token gives full read access to your E3 account. Keep it in Dokploy's environment only. To revoke it, reset the key in E3 under **Preferences → Security keys**, then run `nycu e3-token` again.

## How it works

- **Stateless server**: one Go process serves `/mcp`, the OAuth endpoints and `/healthz`. MCP runs in stateless Streamable HTTP mode.
- **Unauthenticated requests**: `/mcp` without a valid access token returns 401 with a `WWW-Authenticate` header. The header points to `/.well-known/oauth-protected-resource/mcp`, and from there a client discovers the authorization server and registers itself.
- **Signed values**: client IDs, codes and tokens are HMAC-signed with `AUTH_SECRET`. Codes last 1 minute, access tokens 1 hour and refresh tokens 30 days, and refresh tokens rotate. Used codes, used refresh tokens and failed passwords are tracked in memory. After 5 wrong passwords from one address in 15 minutes, that address is locked out for 15 minutes.
- **E3 calls**: the server calls the Moodle Web Services REST API with the token in the POST body. Calls that fan out per course, assignment or quiz run four at a time.
- **Download safety**: `e3p_download_file` sends the token only to `pluginfile.php` URLs on the configured E3 origin over HTTPS. Every redirect is checked against the same rule.

## Limits

- **Single user**: the server is for one person, with one E3 token. To serve more people, deploy one instance each.
- **One replica**: run a single replica. Code replay protection and the password limiter live in memory, and a restart clears them.
- **E3's own data only**: E3 shows only what instructors put there. A deadline announced only in class is not in `e3p_list_due`.
- **No portal or attendance**: the portal and part-time attendance tools of the original plugin are gone.

## For AI agents

Facts for coding agents and LLM tools. The rules for changing the code are in [AGENTS.md](AGENTS.md).

- **Summary**: remote MCP server with 17 read-only tools for NYCU E3 (Moodle) and the public NYCU timetable.
- **Type**: service (Go, Docker)
- **Interfaces**: MCP Streamable HTTP at `/mcp`, protected by OAuth 2.1. `GET /healthz`. CLI `nycu serve | e3-token | healthcheck | version`.
- **Install**: `docker build -t nycu .`, or `go build ./cmd/nycu`
- **Run**: `docker run -p 8080:8080 --env-file .env nycu`
- **Verify**: `curl https://<domain>/healthz`, then call `timetable_list_semesters` from an MCP client
- **Config**: `PUBLIC_URL`, `OWNER_PASSWORD`, `AUTH_SECRET`, `E3_TOKEN`, `E3_USERID`, `E3_BASE`, `PORT`, `E3_DOWNLOAD_MAX_BYTES`, `MCP_MAX_STRING_CHARS`, `MCP_MAX_TOTAL_CHARS`
- **Secrets**: `OWNER_PASSWORD`, `AUTH_SECRET` and `E3_TOKEN`, all in the deployment environment
- **Limits**: one user, one replica, read only

## Development

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

The tests use synthetic data, so CI needs no NYCU account. `NYCU_LIVE=1 go test ./internal/...` also calls the real timetable and E3. With `E3_TOKEN` set, it runs read-only E3 calls too.

Run locally with `PUBLIC_URL=http://localhost:8080` and the other variables, then `go run ./cmd/nycu`.

## License

[MIT](LICENSE). Based on [zyx1121/nycu](https://github.com/zyx1121/nycu).
