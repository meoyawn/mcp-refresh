# mcp-refresh

A Go CLI to list and refresh your development MCP connections in ChatGPT.
Import your signed-in browser session once with
[sweetcookie](https://github.com/steipete/sweetcookie), then run
`mcp-refresh refresh ExampleMCP`. A local MCP caller can use the same session
through the included stdio server.

## Install and use

Requires Go 1.25 or newer and a browser signed in to `https://chatgpt.com`.

```sh
go install github.com/meoyawn/mcp-refresh@latest
mcp-refresh list
mcp-refresh refresh ExampleMCP
mcp-refresh refresh "Example MCP"
```

The binary installs to `$(go env GOPATH)/bin` unless `GOBIN` is configured.
For a build inside this directory, use `go build -o bin/mcp-refresh .`.
From a source checkout, `go install .` installs the local version.

The first list or refresh automatically discovers and imports a working
browser session. To choose a browser/profile or account explicitly:

```sh
mcp-refresh auth import --browser chrome --profile Default
mcp-refresh auth import --browser firefox --profile /path/to/profile
mcp-refresh auth import --browser chrome --account-id YOUR_CHATGPT_WORKSPACE_ID
mcp-refresh auth status
mcp-refresh auth logout
```

sweetcookie supports Chrome, Chromium, Edge, Brave, Vivaldi, Opera, Arc,
Helium, Dia, Comet, Atlas, Whale, Firefox, Zen, Floorp, Waterfox, LibreWolf,
and Safari, subject to platform support. Automatic discovery uses sweetcookie's
default browser order. Use `--browser` for other supported browsers. Cookie
decryption can trigger your OS's Keychain/keyring prompt. `--profile` accepts
a profile name, directory, or cookie database. Browser profiles and Firefox
containers are kept separate when choosing a session.

`list` shows private individual MCP connections (`MCP`, `ONLY_ME`,
`INDIVIDUAL` in the captured API). Published and built-in connections are
excluded. Refresh accepts an exact connector name, connection name, link ID,
or connector ID. Names are case insensitive. For duplicate names or multiple
connections to the same connector, use the link ID from `list`.

```sh
mcp-refresh list --json
mcp-refresh refresh ExampleMCP --json
```

## MCP caller

Run `mcp-refresh serve` as a local stdio MCP server. For example, add this
entry to your MCP client's configuration, using the installed binary's
absolute path:

```json
{
  "mcpServers": {
    "mcp-refresh": {
      "command": "/absolute/path/to/mcp-refresh",
      "args": ["serve"]
    }
  }
}
```

For Codex, the equivalent configuration is:

```toml
[mcp_servers.mcp-refresh]
command = "/absolute/path/to/mcp-refresh"
args = ["serve"]
```

The server exposes two tools:

- `list({})`: list development MCP connections.
- `refresh({"mcp":"ExampleMCP"})`: refresh the named connection and return its tool names.

The user can simply ask **“refresh ExampleMCP”**. The caller supplies only the
MCP name; the local server handles session authentication. Both CLI and MCP
mode use the same session file. Diagnostics go to stderr so stdout stays
valid MCP traffic. This is a local stdio server; a cloud-only ChatGPT client
cannot launch a local executable.

## Session storage

The session lives in `mcp-refresh/session.json` under Go's user configuration
directory (on macOS, `~/Library/Application Support/mcp-refresh/session.json`).
Choose another location with `--session /path/to/session.json` on any command
or the `MCP_REFRESH_SESSION` environment variable.

Cookies are stored locally in an atomically replaced file with mode `0600`;
the created configuration directory uses `0700`. Treat this file as a login
credential. Access tokens are obtained from `/api/auth/session` when needed
and kept in memory. Rotated cookies are saved. If the stored login expires,
the CLI tries importing that browser/profile again before the operation.
If your browser is signed out, sign back in and run `auth import`. Set the
same `--account-id` during import when working in a particular workspace.
`auth logout` deletes the local copy; it does not sign out your browser.

## Backend contract and validation

The client uses these ChatGPT web requests:

| Operation | Request |
| --- | --- |
| List connections | `POST /backend-api/aip/connectors/links/list_accessible` with `{"principals":[],"link_refresh_strategy":"BLOCKING"}` |
| Refresh tools | `POST /backend-api/aip/connectors/mcp/refresh_actions` with `{"link_id":"…"}` |

Refresh success requires an `actions` array in the response, including an
empty array for a server with no tools. A refresh POST is never automatically
retried. Redirects are not followed, and server error bodies are not printed.
Remote tool descriptions and server instructions are treated as data.
Browser captures and real session data are not included or required at runtime.

These web endpoints are undocumented and can change. OpenAI documents the
[Refresh flow for custom MCP connections](https://developers.openai.com/plugins/deploy/connect-chatgpt),
but not this browser-session API. Browser challenges, workspace policies,
or future backend changes may require updating the client.

```sh
go test -race ./...
go vet ./...
```

Tests exercise MCP calls through a local HTTP server, captured request shapes,
connection selection, rotated-cookie persistence, credential-file permissions,
redirect/error handling, and expired-session recovery through sweetcookie
using a temporary Firefox cookie database. They do not access your browser
or account.

## License

[MIT](LICENSE).
