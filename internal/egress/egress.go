// Package egress is the outbound HTTP client for the servers: a timeout, a
// User-Agent, a response-size cap, and (Public) a dialer that refuses
// private, loopback, link-local and other reserved addresses, so a URL from
// third-party data (e.g. a PeeringDB status_dashboard) can't make the server
// reach its own network.
package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// UserAgent identifies the hosted servers to upstream APIs (NWS asks for one).
const UserAgent = "mcp.baldwinsung.com/1.0 (+https://mcp.baldwinsung.com)"

// MaxBody caps a response body.
const MaxBody = 8 << 20

var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/127"), netip.MustParsePrefix("::ffff:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"),
}

// ErrBlocked is returned for a connection to a non-public address.
var ErrBlocked = errors.New("destination is not a public address")

// Public reports whether addr is a globally routable unicast address.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range reserved {
		if p.Contains(addr) {
			return false
		}
	}
	return addr.IsValid()
}

func control(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !Public(ap.Addr()) {
		return fmt.Errorf("%s: %w", address, ErrBlocked)
	}
	return nil
}

// Client is an HTTP client for upstream APIs.
type Client struct {
	HTTP    *http.Client
	Headers map[string]string // sent on every request (e.g. an API key for this client's one upstream)
}

// New returns a client; public=true installs the address guard (and ignores
// proxy settings, which would point at a private address).
func New(public bool) *Client {
	d := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if public {
		d.Control = control
		tr.Proxy = nil
	}
	return &Client{HTTP: &http.Client{
		Transport: tr,
		Timeout:   25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
				return errors.New("redirect to a non-HTTP URL")
			}
			return nil
		},
	}}
}

// StatusError is a non-2xx upstream response.
type StatusError struct {
	URL  string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s: HTTP %d", host(e.URL), e.Code) }

func host(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/?"); i >= 0 {
		u = u[:i]
	}
	return u
}

// Get fetches url and returns the body (at most MaxBody).
func (c *Client) Get(ctx context.Context, url string, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ne net.Error
		if errors.Is(err, ErrBlocked) {
			return nil, fmt.Errorf("%s: %w", host(url), ErrBlocked)
		}
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Errorf("%s: timed out", host(url))
		}
		return nil, fmt.Errorf("%s: %v", host(url), unwrapURL(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", host(url), err)
	}
	if len(body) > MaxBody {
		return nil, fmt.Errorf("%s: response too large", host(url))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return body, &StatusError{url, resp.StatusCode}
	}
	return body, nil
}

func unwrapURL(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) {
		if inner := ue.Unwrap(); inner != nil {
			return inner
		}
	}
	return err
}

// GetJSON fetches url and decodes JSON into v.
func (c *Client) GetJSON(ctx context.Context, url string, v any) error {
	body, err := c.Get(ctx, url, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%s: bad JSON: %v", host(url), err)
	}
	return nil
}
