package vless

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// FindXray returns the path to the xray binary, checking PATH and common locations.
func FindXray() string {
	if p, err := exec.LookPath("xray"); err == nil {
		return p
	}
	candidates := []string{
		"/usr/local/bin/xray",
		"/usr/bin/xray",
		filepath.Join(os.Getenv("HOME"), ".local/bin/xray"),
		"./xray",
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// BuildXrayConfig builds an xray JSON config that exposes a SOCKS5 inbound on socksPort
// and routes outbound traffic via the given VLESS endpoint.
func BuildXrayConfig(cfg *Config, socksPort int) ([]byte, error) {
	user := map[string]any{
		"id":         cfg.UUID,
		"encryption": "none",
	}
	if cfg.Flow != "" {
		user["flow"] = cfg.Flow
	}

	stream := map[string]any{
		"network":  cfg.Transport,
		"security": cfg.Security,
	}

	switch cfg.Transport {
	case "grpc":
		stream["grpcSettings"] = map[string]any{
			"serviceName": cfg.ServiceName,
			"multiMode":   cfg.Mode == "multi",
		}
	case "ws":
		ws := map[string]any{"path": cfg.Path}
		if cfg.WSHost != "" {
			ws["headers"] = map[string]string{"Host": cfg.WSHost}
		}
		stream["wsSettings"] = ws
	case "xhttp", "httpupgrade":
		stream["xhttpSettings"] = map[string]any{"path": cfg.Path, "mode": cfg.Mode}
	}

	switch cfg.Security {
	case "reality":
		stream["realitySettings"] = map[string]any{
			"serverName":  cfg.SNI,
			"fingerprint": cfg.FP,
			"publicKey":   cfg.PBK,
			"shortId":     cfg.SID,
		}
	case "tls":
		stream["tlsSettings"] = map[string]any{
			"serverName":    cfg.SNI,
			"fingerprint":   cfg.FP,
			"allowInsecure": false,
		}
	}

	xrayCfg := map[string]any{
		"log": map[string]any{"loglevel": "none"},
		"inbounds": []any{map[string]any{
			"listen":   "127.0.0.1",
			"port":     socksPort,
			"protocol": "socks",
			"settings": map[string]any{"auth": "noauth", "udp": false},
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": cfg.Host,
					"port":    cfg.Port,
					"users":   []any{user},
				}},
			},
			"streamSettings": stream,
		}},
	}

	return json.MarshalIndent(xrayCfg, "", "  ")
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func waitForPort(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
