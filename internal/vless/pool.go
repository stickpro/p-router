package vless

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Pool manages long-lived xray subprocesses — one per VLESS endpoint.
// For tcp/ws transports without reality it uses the native Go dialer.
// For grpc, xhttp, and reality it delegates to an xray subprocess.
type Pool struct {
	xrayBin string
	mu      sync.Mutex
	procs   map[string]*xrayProc // keyed by raw vless:// URL
}

type xrayProc struct {
	port    int
	cmd     *exec.Cmd
	tmpFile string
}

// NewPool creates a Pool. It searches for xray automatically.
func NewPool() *Pool {
	return &Pool{
		xrayBin: FindXray(),
		procs:   make(map[string]*xrayProc),
	}
}

// NewPoolWithBin creates a Pool using the given xray binary path.
func NewPoolWithBin(bin string) *Pool {
	return &Pool{xrayBin: bin, procs: make(map[string]*xrayProc)}
}

// XrayFound reports whether xray was found.
func (p *Pool) XrayFound() bool { return p.xrayBin != "" }

// Dial connects to destination through the given VLESS endpoint.
// Native Go is used for tcp/ws without reality; xray is used otherwise.
func (p *Pool) Dial(ctx context.Context, rawURL string, cfg *Config, destination string) (net.Conn, error) {
	if isNativeSupported(cfg) {
		return Dial(ctx, cfg, destination)
	}
	if p.xrayBin == "" {
		return nil, fmt.Errorf("vless: transport %q / security %q requires xray (not found in PATH)", cfg.Transport, cfg.Security)
	}
	return p.dialViaXray(ctx, rawURL, cfg, destination)
}

// Close kills all managed xray subprocesses and removes their temp config files.
func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, proc := range p.procs {
		proc.stop()
	}
	p.procs = make(map[string]*xrayProc)
}

// isNativeSupported returns true when the native Go dialer can handle this config.
func isNativeSupported(cfg *Config) bool {
	if cfg.Security == "reality" {
		return false
	}
	switch cfg.Transport {
	case "tcp", "ws", "":
		return true
	default:
		return false
	}
}

func (p *Pool) dialViaXray(ctx context.Context, rawURL string, cfg *Config, destination string) (net.Conn, error) {
	proc, err := p.getOrCreate(rawURL, cfg)
	if err != nil {
		return nil, fmt.Errorf("vless xray: %w", err)
	}

	d, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", proc.port), nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("vless xray socks5 dialer: %w", err)
	}
	conn, err := d.Dial("tcp", destination)
	if err != nil {
		// Process may have died — remove so it gets recreated next time
		p.mu.Lock()
		if stored := p.procs[rawURL]; stored == proc {
			delete(p.procs, rawURL)
			stored.stop()
		}
		p.mu.Unlock()
		return nil, fmt.Errorf("vless xray dial: %w", err)
	}
	return conn, nil
}

func (p *Pool) getOrCreate(rawURL string, cfg *Config) (*xrayProc, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if proc, ok := p.procs[rawURL]; ok {
		if proc.isAlive() {
			return proc, nil
		}
		proc.stop()
		delete(p.procs, rawURL)
	}

	proc, err := p.startXray(cfg)
	if err != nil {
		return nil, err
	}
	p.procs[rawURL] = proc
	return proc, nil
}

func (p *Pool) startXray(cfg *Config) (*xrayProc, error) {
	port, err := freeTCPPort()
	if err != nil {
		return nil, fmt.Errorf("find free port: %w", err)
	}

	cfgBytes, err := BuildXrayConfig(cfg, port)
	if err != nil {
		return nil, fmt.Errorf("build config: %w", err)
	}

	tmp, err := os.CreateTemp("", "xray-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp config: %w", err)
	}
	if _, err := tmp.Write(cfgBytes); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()

	cmd := exec.Command(p.xrayBin, "run", "-c", tmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("start xray: %w", err)
	}

	if !waitForPort(port, 8*time.Second) {
		cmd.Process.Kill() //nolint:errcheck
		cmd.Wait()         //nolint:errcheck
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("xray did not start within 8s")
	}

	return &xrayProc{port: port, cmd: cmd, tmpFile: tmp.Name()}, nil
}

func (pr *xrayProc) isAlive() bool {
	return pr.cmd.ProcessState == nil
}

func (pr *xrayProc) stop() {
	if pr.cmd.Process != nil {
		pr.cmd.Process.Kill() //nolint:errcheck
	}
	pr.cmd.Wait() //nolint:errcheck
	if pr.tmpFile != "" {
		os.Remove(pr.tmpFile)
	}
}
