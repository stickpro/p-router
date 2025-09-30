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
)

type Server struct {
	addr   string
	router *router.ProxyRouter
	server *http.Server
}

func NewServer(addr string, r *router.ProxyRouter) *Server {
	s := &Server{
		addr:   addr,
		router: r,
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
	targetConn, err := net.DialTimeout("tcp", config.Target, 10*time.Second)
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

func (s *Server) handleHTTPRequest(w http.ResponseWriter, r *http.Request, config *router.ProxyConfig) {
	targetConn, err := net.DialTimeout("tcp", config.Target, 10*time.Second)
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
