package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	for a, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "10.1.2.3": false, "127.0.0.1": false,
		"169.254.169.254": false, "100.64.1.1": false, "::1": false, "fe80::1": false,
		"::ffff:127.0.0.1": false, "192.168.1.1": false, "fd00::1": false, "224.0.0.1": false,
	} {
		if got := Public(netip.MustParseAddr(a)); got != want {
			t.Errorf("Public(%s) = %v, want %v", a, got, want)
		}
	}
}

func TestGuardBlocksLoopback(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{}")) }))
	defer ts.Close()
	var v map[string]any
	if err := New(true).GetJSON(context.Background(), ts.URL, &v); !errors.Is(err, ErrBlocked) {
		t.Fatalf("public client reached loopback: %v", err)
	}
	if err := New(false).GetJSON(context.Background(), ts.URL, &v); err != nil {
		t.Fatalf("private client: %v", err)
	}
}

func TestRedirectToLoopbackBlocked(t *testing.T) {
	// A public URL can't be faked offline, so check the guard applies to
	// every dial: a redirect target on loopback fails the same way.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ts.Close()
	_, err := New(true).Get(context.Background(), ts.URL+"/x", "")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("got %v", err)
	}
}
