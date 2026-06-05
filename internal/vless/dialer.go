package vless

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Dial connects to destination (host:port) through the VLESS server described by cfg.
// It performs the VLESS handshake and returns a net.Conn ready for bidirectional use.
func Dial(ctx context.Context, cfg *Config, destination string) (net.Conn, error) {
	switch cfg.Transport {
	case "tcp", "":
		return dialTCP(ctx, cfg, destination)
	case "ws":
		return dialWS(ctx, cfg, destination)
	case "grpc":
		return nil, fmt.Errorf("vless: grpc transport not supported natively — import the proxy via xray SOCKS5")
	case "xhttp", "httpupgrade":
		return nil, fmt.Errorf("vless: %s transport not supported natively — import the proxy via xray SOCKS5", cfg.Transport)
	default:
		return nil, fmt.Errorf("vless: unknown transport %q", cfg.Transport)
	}
}

func dialTCP(ctx context.Context, cfg *Config, destination string) (net.Conn, error) {
	conn, err := dialWithSecurity(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("vless tcp: connect: %w", err)
	}
	if err := handshake(conn, cfg, destination); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func dialWS(ctx context.Context, cfg *Config, destination string) (net.Conn, error) {
	raw, err := dialWithSecurity(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("vless ws: connect: %w", err)
	}
	ws, err := upgradeWebSocket(raw, cfg)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("vless ws: upgrade: %w", err)
	}
	if err := handshake(ws, cfg, destination); err != nil {
		ws.Close()
		return nil, err
	}
	return ws, nil
}

func dialWithSecurity(ctx context.Context, cfg *Config) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	addr := cfg.ServerAddr()

	switch cfg.Security {
	case "none", "":
		return d.DialContext(ctx, "tcp", addr)
	case "tls":
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			ServerName: cfg.SNI,
			MinVersion: tls.VersionTLS12,
		})
	case "reality":
		// REALITY requires xray's custom ClientHello extension to identify itself
		// to the server. Without it the server routes the TLS connection to the
		// real SNI host. We attempt standard TLS with InsecureSkipVerify as a
		// best-effort; this only works when the server has fallback routing off.
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			ServerName:         cfg.SNI,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true, //nolint:gosec
		})
	default:
		return nil, fmt.Errorf("unknown security %q", cfg.Security)
	}
}

// handshake writes the VLESS request header and reads the server response header.
// After this the conn can be used for raw bidirectional data transfer.
func handshake(conn net.Conn, cfg *Config, destination string) error {
	host, portStr, err := net.SplitHostPort(destination)
	if err != nil {
		return fmt.Errorf("vless: bad destination %q: %w", destination, err)
	}

	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	uuid, err := parseUUID(cfg.UUID)
	if err != nil {
		return fmt.Errorf("vless: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteByte(0x00)  // version
	buf.Write(uuid[:])   // UUID (16 bytes)
	buf.WriteByte(0x00)  // addons length = 0
	buf.WriteByte(0x01)  // command: TCP stream
	binary.Write(&buf, binary.BigEndian, uint16(port)) //nolint:errcheck

	ip := net.ParseIP(host)
	switch {
	case ip == nil:
		buf.WriteByte(0x02)
		buf.WriteByte(byte(len(host)))
		buf.WriteString(host)
	case ip.To4() != nil:
		buf.WriteByte(0x01)
		buf.Write(ip.To4())
	default:
		buf.WriteByte(0x03)
		buf.Write(ip.To16())
	}

	if _, err := conn.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("vless: write header: %w", err)
	}

	// Response: [version 1B][addons-len 1B][addons nB]
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("vless: read response: %w", err)
	}
	if resp[1] > 0 {
		discard := make([]byte, resp[1])
		if _, err := io.ReadFull(conn, discard); err != nil {
			return fmt.Errorf("vless: read response addons: %w", err)
		}
	}
	return nil
}

func parseUUID(s string) ([16]byte, error) {
	s = strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return [16]byte{}, fmt.Errorf("invalid UUID %q", s)
	}
	var uuid [16]byte
	copy(uuid[:], b)
	return uuid, nil
}
