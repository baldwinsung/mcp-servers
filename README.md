# mcp-servers

Three small [MCP](https://modelcontextprotocol.io) servers that give an AI
assistant like Claude live answers to everyday questions:

| Server | Ask it things like | Tools |
|---|---|---|
| **weather** | "What's the weather in Portland, OR this weekend?" · "Any storm warnings in Miami?" | `weather_forecast`, `weather_alerts`, `geocode` |
| **cloudstatus** | "Is Outlook down?" · "Is anything having an outage right now?" | `service_status`, `current_outages`, `list_services` |
| **ixstatus** | "Is NYIIX having problems?" · "Which internet exchanges are in Frankfurt?" | `ix_status`, `find_ix` |

They're free to use at `https://mcp.baldwinsung.com`, or you can run your own.
Every data source is free and public. [SOURCES.md](SOURCES.md) lists them all.

## Quick start

Add them to Claude Code:

```bash
claude mcp add --transport http weather     https://mcp.baldwinsung.com/weather
claude mcp add --transport http cloudstatus https://mcp.baldwinsung.com/cloudstatus
claude mcp add --transport http ixstatus    https://mcp.baldwinsung.com/ixstatus
```

For Claude Desktop, claude.ai or another client, add a custom connector with
one of those URLs.

**Free use:** 50 tool calls per IP address, shared across all three servers.
For more, request a free personal token at
[mcp.baldwinsung.com/request](https://mcp.baldwinsung.com/request). After approval it's shown
on your request's status page and emailed to you.

[GETTING_STARTED.md](GETTING_STARTED.md) has the rest: using a token,
building and running your own copies, example calls and troubleshooting.

## Good to know

- **weather** can't tell where you are, so give it a place name or
  coordinates. Weather alerts cover the US and its territories only.
- **cloudstatus** covers 32 services, including Microsoft 365, Google,
  AWS, Azure, Slack, GitHub, Zoom and OpenAI. Ask by product name too
  (`gmail`, `outlook`, `teams`). Business Microsoft 365 tenants have no
  public status feed, so only consumer services and admin-center notices
  are covered.
- **ixstatus** uses PeeringDB for exchange details. Most exchanges publish
  no machine-readable status, so for those it can only say whether their
  web pages respond. It says so in its answer.

## How it works

Each server is a single Go binary using only the standard library. It
speaks MCP over HTTP (Streamable HTTP, stateless) and listens on
`127.0.0.1` only. The hosted copies sit behind a separate gateway that
handles TLS, the free allowance, tokens and rate limits.

| Directory | What's in it |
|---|---|
| `cmd/mcp-weather`, `cmd/mcp-cloudstatus`, `cmd/mcp-ixstatus` | One server each |
| `internal/mcp` | The shared MCP layer: JSON-RPC, HTTP handling, a per-tool cache, a limit of 8 concurrent calls |
| `internal/egress` | The one HTTP client for all outbound calls: 25 s timeout, 8 MiB response cap. With `MCP_PUBLIC=1` it refuses private and reserved addresses, because ixstatus follows URLs taken from PeeringDB |
| `internal/textutil` | HTML to plain text |
| `deploy/` | Hardened systemd units (throwaway user, 128 MB memory cap) |

## Develop

```bash
go vet ./... && go test -race ./...   # tests run offline
MCP_LISTEN_ADDR=127.0.0.1:19103 go run ./cmd/mcp-cloudstatus
```

Needs Go 1.26+. Build release binaries with `CGO_ENABLED=0 go build ./cmd/...`.

## License

MIT, see [LICENSE](LICENSE).
