package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/stickpro/p-router/internal/router"
	"github.com/stickpro/p-router/internal/vless"
	"golang.org/x/net/proxy"
)

type Server struct {
	addr   string
	router *router.ProxyRouter
	pool   *vless.Pool
	server *http.Server
}

func NewServer(addr string, r *router.ProxyRouter, pool *vless.Pool) *Server {
	s := &Server{
		addr:   addr,
		router: r,
		pool:   pool,
	}

	s.server = &http.Server{
		Addr:         addr,
		Handler:      http.HandlerFunc(s.handleHTTP),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

func (s *Server) Start() error {
	return s.server.ListenAndServe()
}

func (s *Server) Stop(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func parseProxyAuth(authHeader string) (string, string, bool) {
	if authHeader == "" {
		return "", "", false
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Basic" {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", false
	}

	credentials := strings.SplitN(string(decoded), ":", 2)
	if len(credentials) != 2 {
		return "", "", false
	}

	return credentials[0], credentials[1], true
}

func dialViaSocks5(config *router.ProxyConfig, destination string) (net.Conn, error) {
	var auth *proxy.Auth
	if config.ProxyUser != "" {
		auth = &proxy.Auth{User: config.ProxyUser, Password: config.ProxyPass}
	}
	dialer, err := proxy.SOCKS5("tcp", config.TargetAddr, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("failed to create SOCKS5 dialer: %w", err)
	}
	return dialer.Dial("tcp", destination)
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, ok := parseProxyAuth(r.Header.Get("Proxy-Authorization"))
	if !ok {
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}

	config, valid := s.router.GetProxy(username, password)
	if !valid {
		w.Header().Set("Proxy-Authenticate", "Basic realm=\"Proxy\"")
		http.Error(w, "Invalid credentials", http.StatusProxyAuthRequired)
		return
	}

	if r.Method == http.MethodConnect {
		s.handleConnect(w, r, config)
	} else {
		s.handleHTTPRequest(w, r, config)
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	if config.Protocol == "vless" {
		s.handleConnectViaVLESS(w, r, config)
		return
	}
	if config.Protocol == "socks5" {
		s.handleConnectViaSocks5(w, r, config)
		return
	}

	targetConn, err := net.DialTimeout("tcp", config.TargetAddr, 10*time.Second)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cannot connect to proxy: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", r.Host, r.Host)
	_, err = targetConn.Write([]byte(connectReq))
	if err != nil {
		http.Error(w, "Failed to send CONNECT", http.StatusInternalServerError)
		return
	}

	reader := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(reader, r)
	if err != nil {
		http.Error(w, "Failed to read proxy response", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Proxy returned: %s", resp.Status), resp.StatusCode)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go io.Copy(targetConn, clientConn)
	io.Copy(clientConn, targetConn)
}

func (s *Server) handleConnectViaSocks5(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	targetConn, err := dialViaSocks5(config, r.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cannot connect via SOCKS5: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go io.Copy(targetConn, clientConn)
	io.Copy(clientConn, targetConn)
}

func (s *Server) handleHTTPRequest(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	if config.Protocol == "vless" {
		s.handleHTTPRequestViaVLESS(w, r, config)
		return
	}
	if config.Protocol == "socks5" {
		s.handleHTTPRequestViaSocks5(w, r, config)
		return
	}

	targetConn, err := net.DialTimeout("tcp", config.TargetAddr, 10*time.Second)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cannot connect to proxy: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	if err := r.Write(targetConn); err != nil {
		http.Error(w, "Failed to send request to proxy", http.StatusInternalServerError)
		return
	}

	reader := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(reader, r)
	if err != nil {
		http.Error(w, "Failed to read proxy response", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) handleHTTPRequestViaSocks5(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	destination := r.URL.Host
	if destination == "" {
		destination = r.Host
	}
	if !strings.Contains(destination, ":") {
		destination += ":80"
	}

	targetConn, err := dialViaSocks5(config, destination)
	if err != nil {
		http.Error(w, fmt.Sprintf("Cannot connect via SOCKS5: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")

	// Convert proxy-style request (absolute URI) to direct-style (relative URI)
	r.RequestURI = ""
	r.URL.Scheme = ""
	r.URL.Host = ""

	if err := r.Write(targetConn); err != nil {
		http.Error(w, "Failed to send request", http.StatusInternalServerError)
		return
	}

	reader := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(reader, r)
	if err != nil {
		http.Error(w, "Failed to read response", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ─── VLESS handlers ──────────────────────────────────────────────────────────

func (s *Server) handleConnectViaVLESS(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	if config.VLESS == nil {
		http.Error(w, "VLESS config is invalid", http.StatusInternalServerError)
		return
	}
	targetConn, err := s.pool.Dial(r.Context(), config.Target, config.VLESS, r.Host)
	if err != nil {
		http.Error(w, fmt.Sprintf("VLESS dial failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer clientConn.Close()

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")) //nolint:errcheck
	go io.Copy(targetConn, clientConn)
	io.Copy(clientConn, targetConn)
}

func (s *Server) handleHTTPRequestViaVLESS(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	if config.VLESS == nil {
		http.Error(w, "VLESS config is invalid", http.StatusInternalServerError)
		return
	}

	destination := r.URL.Host
	if destination == "" {
		destination = r.Host
	}
	if !strings.Contains(destination, ":") {
		destination += ":80"
	}

	targetConn, err := s.pool.Dial(r.Context(), config.Target, config.VLESS, destination)
	if err != nil {
		http.Error(w, fmt.Sprintf("VLESS dial failed: %v", err), http.StatusServiceUnavailable)
		return
	}
	defer targetConn.Close()

	r.Header.Del("Proxy-Authorization")
	r.Header.Del("Proxy-Connection")
	r.RequestURI = ""
	r.URL.Scheme = ""
	r.URL.Host = ""

	if err := r.Write(targetConn); err != nil {
		http.Error(w, "Failed to send request", http.StatusInternalServerError)
		return
	}

	vlessReader := bufio.NewReader(targetConn)
	vlessResp, err := http.ReadResponse(vlessReader, r)
	if err != nil {
		http.Error(w, "Failed to read response", http.StatusInternalServerError)
		return
	}
	defer vlessResp.Body.Close()

	for key, values := range vlessResp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(vlessResp.StatusCode)
	io.Copy(w, vlessResp.Body) //nolint:errcheck
}
