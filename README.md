# mcp-servers

Public MCP servers behind `mcp.baldwinsung.com`: weather, cloud-app outage
status and internet-exchange status. Each one is a small Go binary with no
dependencies outside the standard library. It speaks MCP Streamable HTTP on
loopback, with a separate gateway in front handling TLS, the free call allowance,
tokens and rate limits.

**New here? See [GETTING_STARTED.md](GETTING_STARTED.md)** for copy-and-paste
setup: the hosted servers in Claude, or building and running your own.
**[SOURCES.md](SOURCES.md)** lists every data source each server uses.

| Path | Binary | Port | Tools | Sources |
|---|---|---|---|---|
| `/weather` | `mcp-weather` | 127.0.0.1:9102 | `weather_forecast`, `weather_alerts`, `geocode` | Open-Meteo forecast + geocoding (no key, CC BY 4.0), US NWS `api.weather.gov` alerts |
| `/cloudstatus` | `mcp-cloudstatus` | 127.0.0.1:9103 | `service_status`, `current_outages`, `list_services` | 32 providers' own status feeds (below) |
| `/ixstatus` | `mcp-ixstatus` | 127.0.0.1:9104 | `ix_status`, `find_ix` | PeeringDB + each exchange's own pages |

## Layout

| Path | What |
|---|---|
| `internal/mcp` | JSON-RPC + Streamable HTTP (stateless, POST only, 202 for notifications, no batches, 64 KiB bodies), per-tool result cache, 8 concurrent calls, argument helpers. `Run` reads `MCP_LISTEN_ADDR`, `MCP_PUBLIC` |
| `internal/egress` | Outbound client: 25 s timeout, User-Agent `mcp.baldwinsung.com/1.0`, 8 MiB body cap. With `MCP_PUBLIC=1` the dialer refuses private/loopback/link-local/reserved addresses and ignores proxy settings |
| `internal/textutil` | HTML to plain text, trimming |
| `cmd/mcp-*` | One server each; `deploy/mcp-*.service` are their hardened systemd units (DynamicUser, 128 MB) |

## Notes per server

**weather.** The server can't locate the user: behind the gateway the caller's
IP is usually the MCP client's platform (e.g. Anthropic), not the person, so
tools take a place name or coordinates. Open-Meteo matches city names only, so
`"Portland, OR"` searches `Portland` and filters on the rest (US state
abbreviations understood). Alerts only cover the US and its territories.

**cloudstatus.** Fixed URLs only; nothing from the caller reaches a URL.
- Atlassian Statuspage `/api/v2/summary.json`: GitHub, Zoom (zoomstatus.com),
  Dropbox, Discord, OpenAI, Claude, Cloudflare, Atlassian/Jira/Confluence/
  Bitbucket/Trello, Reddit, Twilio, DigitalOcean, npm, Notion, Figma, HubSpot,
  Box, Shopify, Vercel, Netlify, Akamai (Linode), Asana.
- Slack `slack-status.com/api/v2.0.0/current`.
- Microsoft 365: `status.cloud.microsoft/api/posts/m365Consumer` (the JSON
  behind Microsoft's status page: Outlook.com, OneDrive, Teams Free, Office
  web, Copilot...) and `/api/posts/mac` (the admin-center banner). Business
  tenant health has no public feed; the tool says so.
- Azure: RSS of active incidents. Google Workspace and Google Cloud:
  `incidents.json` (active = no `end`). AWS: Health Dashboard `currentevents`,
  served as UTF-16 with a BOM.
- Product aliases filter multi-product feeds (`gmail`, `outlook`, `teams`...).
- Checked 2026-11-17: Datadog, Salesforce, Fastly, Okta (403/401), GitLab,
  Stripe, Heroku, Zendesk, Vultr, Hetzner, RingCentral (404) and Linear,
  Docker, Webex (not Statuspage) have no usable public JSON.

**ixstatus.** PeeringDB for directory data. Health: if the exchange's
`status_dashboard` is a Statuspage its indicator and incidents are used,
otherwise whether its dashboard, website and stats pages answer (`health:
no_known_issues` / `pages_unreachable`; a 404 is flagged as a stale link, not an
outage). Most exchanges, NYIIX included, publish no machine-readable status
(NYIIX's PeeringDB stats URL is a 404). Those URLs are third-party data, hence
the egress guard. **PeeringDB throttles anonymous clients to about one large
query an hour**, so the full exchange list (for typo matching, "NYIXX" ->
NYIIX) refreshes daily and searches fall back to PeeringDB's own filters;
detail records cache for an hour. A free PeeringDB API key in
`/etc/mcp-gateway/ixstatus.env` (`PEERINGDB_API_KEY=...`) lifts the limit.
IODA was considered and left out: its responses are "All Rights Reserved".

## Develop

```bash
go vet ./... && go test -race ./...   # offline: tests use httptest fixtures
MCP_LISTEN_ADDR=127.0.0.1:19103 go run ./cmd/mcp-cloudstatus
```

Set `MCP_PUBLIC=1` in production. Leave it unset only when outbound traffic
must go through an HTTP proxy (`HTTPS_PROXY`), since public mode ignores proxies.
Build with `CGO_ENABLED=0 go build ./cmd/...` and install the units from `deploy/`.

## License

MIT, see `LICENSE`.
