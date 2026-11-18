# Getting started

Three MCP servers: **weather**, **cloudstatus** (is Outlook, Slack, GitHub or
AWS down?) and **ixstatus** (internet exchanges, like NYIIX or DE-CIX).
You can use the hosted copies right away, or build and run your own.

## 1. Use the hosted servers (no install)

The servers are live at `https://mcp.baldwinsung.com`. You get 50 free tool
calls per IP, shared across all its servers. After that, request a free
personal token at [mcp.baldwinsung.com](https://mcp.baldwinsung.com).

### Claude Code

```bash
claude mcp add --transport http weather     https://mcp.baldwinsung.com/weather
claude mcp add --transport http cloudstatus https://mcp.baldwinsung.com/cloudstatus
claude mcp add --transport http ixstatus    https://mcp.baldwinsung.com/ixstatus
```

Then ask things like *"What's the weather in Portland, OR this weekend?"*,
*"Is Outlook down?"* or *"Is NYIIX having problems?"*.

With a token, send it as a header:

```bash
claude mcp add --transport http weather https://mcp.baldwinsung.com/weather \
  --header "Authorization: Bearer YOUR_TOKEN"
```

### Claude Desktop, claude.ai and other clients

Add a custom (remote) connector with one of these URLs:

```text
https://mcp.baldwinsung.com/weather
https://mcp.baldwinsung.com/cloudstatus
https://mcp.baldwinsung.com/ixstatus
```

If your client can't send headers, put the token in the path instead:
`https://mcp.baldwinsung.com/weather/t/YOUR_TOKEN`.

### Try it with curl

```bash
curl -s https://mcp.baldwinsung.com/cloudstatus \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"service_status","arguments":{"service":"outlook"}}}'
```

Each `tools/call` counts toward the free 50. `tools/list` is free.

## 2. Run your own

You need Go 1.26 or newer and git.

```bash
git clone https://github.com/baldwinsung/mcp-servers.git
cd mcp-servers
go test ./...                      # offline, no network needed
CGO_ENABLED=0 go build -o bin/ ./cmd/...
ls bin/                            # mcp-cloudstatus  mcp-ixstatus  mcp-weather
```

Start all three, one per terminal or in the background:

```bash
MCP_PUBLIC=1 MCP_LISTEN_ADDR=127.0.0.1:9102 bin/mcp-weather &
MCP_PUBLIC=1 MCP_LISTEN_ADDR=127.0.0.1:9103 bin/mcp-cloudstatus &
MCP_PUBLIC=1 MCP_LISTEN_ADDR=127.0.0.1:9104 bin/mcp-ixstatus &
```

Check that they're up:

```bash
curl -s 127.0.0.1:9102/healthz; curl -s 127.0.0.1:9103/healthz; curl -s 127.0.0.1:9104/healthz
```

Connect Claude Code to your local copies:

```bash
claude mcp add --transport http weather     http://127.0.0.1:9102/
claude mcp add --transport http cloudstatus http://127.0.0.1:9103/
claude mcp add --transport http ixstatus    http://127.0.0.1:9104/
```

Stop them with `kill %1 %2 %3`, or `pkill -f bin/mcp-`.

### Settings

| Variable | Meaning |
|---|---|
| `MCP_LISTEN_ADDR` | Address to listen on. Defaults: weather `127.0.0.1:9102`, cloudstatus `:9103`, ixstatus `:9104` |
| `MCP_PUBLIC=1` | Blocks requests to private and reserved IP addresses. Use it on anything reachable from the internet. It also ignores `HTTPS_PROXY`, so leave it unset if your machine can only reach the internet through a proxy |
| `PEERINGDB_API_KEY` | Optional, for ixstatus. PeeringDB allows anonymous clients about one large query an hour; a key from a free [PeeringDB](https://www.peeringdb.com) account lifts that |

The servers have no authentication of their own and listen on loopback.
To expose one publicly, put a reverse proxy in front that handles TLS and
access control.

### Run as a systemd service (Linux)

```bash
sudo install -m 0755 bin/mcp-weather bin/mcp-cloudstatus bin/mcp-ixstatus /usr/local/bin/
sudo cp deploy/mcp-*.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now mcp-weather mcp-cloudstatus mcp-ixstatus
systemctl status mcp-weather mcp-cloudstatus mcp-ixstatus --no-pager
```

The units run each server as a throwaway user with a 128 MB memory cap. For
the ixstatus API key:

```bash
sudo mkdir -p /etc/mcp-gateway
echo 'PEERINGDB_API_KEY=your-key' | sudo tee /etc/mcp-gateway/ixstatus.env >/dev/null
sudo chmod 600 /etc/mcp-gateway/ixstatus.env
sudo systemctl restart mcp-ixstatus
```

## 3. Example calls

Swap the port for whichever server you're calling. Against the hosted copy,
use `https://mcp.baldwinsung.com/<name>` instead.

```bash
mcp() { curl -s "$1" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"$2\",\"arguments\":$3}}"; echo; }

# weather
mcp http://127.0.0.1:9102/ weather_forecast '{"place":"Portland, OR","days":5}'
mcp http://127.0.0.1:9102/ weather_forecast '{"latitude":40.71,"longitude":-74.01}'
mcp http://127.0.0.1:9102/ weather_alerts   '{"place":"Miami, FL"}'
mcp http://127.0.0.1:9102/ geocode          '{"place":"Paris","count":3}'

# cloudstatus
mcp http://127.0.0.1:9103/ service_status  '{"service":"outlook"}'
mcp http://127.0.0.1:9103/ current_outages '{}'
mcp http://127.0.0.1:9103/ list_services   '{}'

# ixstatus
mcp http://127.0.0.1:9104/ ix_status '{"ix":"NYIIX"}'
mcp http://127.0.0.1:9104/ find_ix   '{"query":"Frankfurt","country":"DE"}'
```

List a server's tools and their arguments:

```bash
curl -s http://127.0.0.1:9103/ -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
```

## Troubleshooting

| Symptom | Fix |
|---|---|
| `connection refused` | The server isn't running, or is on another port. Check `MCP_LISTEN_ADDR` and the `curl .../healthz` output |
| Every upstream call fails with a dial error | You're behind a proxy with `MCP_PUBLIC=1` set. Unset it for local use |
| ixstatus says PeeringDB is rate-limiting | Wait an hour, or set `PEERINGDB_API_KEY` |
| Hosted server says the free calls are used up | Request a free token at [mcp.baldwinsung.com](https://mcp.baldwinsung.com) |
| `go: go.mod requires go >= 1.26` | Install Go 1.26+, or run with `GOTOOLCHAIN=go1.26.11` |

See [README.md](README.md) for the data sources and how each server works.
