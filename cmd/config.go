package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

type Config struct {
	Addr string `json:"addr"`

	// TrustedProxies are the addresses whose X-Forwarded-For header may be
	// believed. Empty means none, so the client address is the socket's peer.
	//
	// This is a security setting, not a convenience: gin's own default trusts
	// every proxy, and X-Forwarded-For is a request header anyone can write. With
	// it trusted, the per-address rate limit on public invoice creation is
	// bypassed by sending a different value each time. Behind nginx, list nginx.
	TrustedProxies []string `json:"trusted_proxies"`
}

func LoadConfig(f *config.File) (Config, error) {
	cfg := Config{Addr: ":8080"}
	if err := f.Section("app", &cfg); err != nil {
		return cfg, err
	}

	// Validated here so a typo is a startup failure with a line number, not a
	// silently ignored entry that leaves a proxy untrusted and every client
	// looking like the proxy's own address.
	for i, p := range cfg.TrustedProxies {
		p = strings.TrimSpace(p)
		if p == "" {
			return cfg, fmt.Errorf("config: app.trusted_proxies[%d] is empty", i)
		}
		if strings.Contains(p, "/") {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return cfg, fmt.Errorf("config: app.trusted_proxies[%d] = %q is not a valid CIDR", i, p)
			}
			continue
		}
		if net.ParseIP(p) == nil {
			return cfg, fmt.Errorf("config: app.trusted_proxies[%d] = %q is not an IP address or CIDR", i, p)
		}
	}

	return cfg, nil
}

func waitForShutdown(fn func()) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	fn()
}
