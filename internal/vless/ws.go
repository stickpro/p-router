package vless

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
)

// wsConn wraps a net.Conn with WebSocket binary framing.
// Client→server frames are masked (required by RFC 6455).
// Server→client frames are expected to be unmasked.
type wsConn struct {
	net.Conn
	buf []byte // buffered leftover payload from last read frame
}

func (w *wsConn) Write(p []byte) (int, error) {
	if _, err := w.Conn.Write(marshalWSFrame(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *wsConn) Read(p []byte) (int, error) {
	if len(w.buf) > 0 {
		n := copy(p, w.buf)
		w.buf = w.buf[n:]
		return n, nil
	}
	payload, err := unmarshalWSFrame(w.Conn)
	if err != nil {
		return 0, err
	}
	n := copy(p, payload)
	if n < len(payload) {
		w.buf = payload[n:]
	}
	return n, nil
}

// upgradeWebSocket performs the HTTP/1.1 → WebSocket upgrade handshake on conn.
func upgradeWebSocket(conn net.Conn, cfg *Config) (*wsConn, error) {
	host := cfg.WSHost
	if host == "" {
		host = cfg.SNI
	}
	if host == "" {
		host = cfg.Host
	}
	path := cfg.Path
	if path == "" {
		path = "/"
	}

	key := make([]byte, 16)
	rand.Read(key) //nolint:errcheck
	wsKey := base64.StdEncoding.EncodeToString(key)

	upgradeReq := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, host, wsKey,
	)
	if _, err := conn.Write([]byte(upgradeReq)); err != nil {
		return nil, fmt.Errorf("write upgrade request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}

	return &wsConn{Conn: conn}, nil
}

// marshalWSFrame builds a masked binary WebSocket frame (opcode 0x02, FIN=1).
func marshalWSFrame(payload []byte) []byte {
	l := len(payload)
	var hdr []byte
	switch {
	case l < 126:
		hdr = []byte{0x82, byte(l) | 0x80}
	case l <= 0xFFFF:
		hdr = []byte{0x82, 0xFE, byte(l >> 8), byte(l)}
	default:
		hdr = []byte{0x82, 0xFF, 0, 0, 0, 0, byte(l >> 24), byte(l >> 16), byte(l >> 8), byte(l)}
	}

	var mask [4]byte
	rand.Read(mask[:]) //nolint:errcheck

	frame := make([]byte, len(hdr)+4+l)
	copy(frame, hdr)
	copy(frame[len(hdr):], mask[:])
	for i, b := range payload {
		frame[len(hdr)+4+i] = b ^ mask[i%4]
	}
	return frame
}

// unmarshalWSFrame reads one complete WebSocket frame from r.
func unmarshalWSFrame(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}

	opcode := hdr[0] & 0x0F
	if opcode == 8 { // close frame
		return nil, io.EOF
	}

	masked := (hdr[1] & 0x80) != 0
	payloadLen := int(hdr[1] & 0x7F)

	switch payloadLen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		payloadLen = int(ext[0])<<8 | int(ext[1])
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		payloadLen = int(ext[4])<<24 | int(ext[5])<<16 | int(ext[6])<<8 | int(ext[7])
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(r, maskKey[:]); err != nil {
			return nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, nil
}
