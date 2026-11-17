package mcp

import (
	"log"
	"net/http"
	"os"
	"time"
)

// Run serves s on MCP_LISTEN_ADDR (default def; loopback only by design).
func Run(s *Server, def string) {
	log.SetFlags(0)
	addr := os.Getenv("MCP_LISTEN_ADDR")
	if addr == "" {
		addr = def
	}
	srv := &http.Server{
		Addr: addr, Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 120 * time.Second,
		ErrorLog: log.New(os.Stderr, "", 0),
	}
	log.Printf("%s %s listening on %s (public=%v)", s.Name, s.Version, addr, PublicMode())
	log.Fatal(srv.ListenAndServe())
}

// PublicMode is MCP_PUBLIC=1: internet-facing, egress guard on.
func PublicMode() bool { return os.Getenv("MCP_PUBLIC") == "1" }
