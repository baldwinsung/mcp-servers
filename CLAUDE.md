# mcp-servers

Public repo. The weather, cloudstatus and ixstatus MCP backends of
`mcp.baldwinsung.com`; README.md is the reference. Keep deployment and
host-specific details out of this repo.

- Backends listen on **127.0.0.1 only**; the gateway is their sole client.
- Every outbound request goes through `internal/egress`. Never make an HTTP
  call with another client: in public mode egress is what stops URLs from
  third-party data (PeeringDB) reaching private addresses.
- Never put a caller-supplied value into a URL's host. Arguments go in query
  strings via `url.Values`.
- Bad input and "not found" are `mcp.Errorf` (tool errors with a helpful
  message); upstream failures are plain errors.
- Tests must not need the network.
- Build with Go 1.26+, `CGO_ENABLED=0`.
