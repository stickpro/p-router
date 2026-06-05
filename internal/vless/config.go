package vless

import (
	"fmt"
	"net/url"
	"strconv"
)

// Config holds the parsed parameters of a vless:// URL.
type Config struct {
	UUID        string
	Host        string
	Port        int
	Security    string // none | tls | reality
	Transport   string // tcp | ws | grpc | xhttp
	SNI         string
	FP          string
	PBK         string
	SID         string
	Path        string
	ServiceName string
	Mode        string
	WSHost      string
	Flow        string
}

// ServerAddr returns "host:port".
func (c *Config) ServerAddr() string {
	return c.Host + ":" + strconv.Itoa(c.Port)
}

// ParseConfig parses a vless:// URL into a Config.
func ParseConfig(raw string) (*Config, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("vless: invalid URL: %w", err)
	}
	if u.Scheme != "vless" {
		return nil, fmt.Errorf("vless: expected scheme vless, got %q", u.Scheme)
	}

	port, err := strconv.Atoi(u.Port())
	if err != nil {
		return nil, fmt.Errorf("vless: invalid port: %w", err)
	}

	q := u.Query()

	security := q.Get("security")
	if security == "" || security == "false" {
		security = "none"
	}
	transport := q.Get("type")
	if transport == "" {
		transport = "tcp"
	}

	return &Config{
		UUID:        u.User.Username(),
		Host:        u.Hostname(),
		Port:        port,
		Security:    security,
		Transport:   transport,
		SNI:         q.Get("sni"),
		FP:          q.Get("fp"),
		PBK:         q.Get("pbk"),
		SID:         q.Get("sid"),
		Path:        q.Get("path"),
		ServiceName: q.Get("serviceName"),
		Mode:        q.Get("mode"),
		WSHost:      q.Get("host"),
		Flow:        q.Get("flow"),
	}, nil
}
