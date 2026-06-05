package console

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/stickpro/p-router/internal/vless"
	"github.com/urfave/cli/v3"
	"golang.org/x/net/proxy"
)

// checkedVless pairs a parsed VLESS config with its URL fragment (display label).
type checkedVless struct {
	*vless.Config
	Label string
}

type vlessResult struct {
	EP         *checkedVless
	ServerIP   string
	ExternalIP string
	Latency    time.Duration
	Alive      bool
	Err        string
}

func vlessCheckCommand() *cli.Command {
	return &cli.Command{
		Name:        "vless-check",
		Description: "Check VLESS configs: test TCP connectivity, resolve server IP, and optionally get external IP via xray",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   "File with VLESS URLs (one per line). Reads stdin if omitted.",
			},
			&cli.BoolFlag{
				Name:    "external-ip",
				Aliases: []string{"e"},
				Usage:   "Fetch external IP through each working proxy using xray (requires xray in PATH or common locations)",
			},
			&cli.IntFlag{
				Name:    "workers",
				Aliases: []string{"w"},
				Usage:   "Number of parallel check workers",
				Value:   10,
			},
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "TCP dial timeout per proxy",
				Value:   5 * time.Second,
			},
			&cli.BoolFlag{
				Name:  "alive-only",
				Usage: "Show only reachable proxies",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			var reader io.Reader
			if f := cmd.String("file"); f != "" {
				file, err := os.Open(f)
				if err != nil {
					return fmt.Errorf("open file: %w", err)
				}
				defer file.Close()
				reader = file
			} else {
				reader = os.Stdin
			}

			endpoints, err := parseVlessFile(reader)
			if err != nil {
				return err
			}
			if len(endpoints) == 0 {
				fmt.Println("no VLESS URLs found")
				return nil
			}

			fmt.Printf("checking %d endpoints (workers=%d, timeout=%s)\n\n",
				len(endpoints), cmd.Int("workers"), cmd.Duration("timeout"))

			results := checkVlessEndpoints(ctx, endpoints, cmd.Int("workers"), cmd.Duration("timeout"))

			if cmd.Bool("external-ip") {
				xrayBin := findXray()
				if xrayBin == "" {
					fmt.Println("warning: xray not found in PATH or common locations — skipping external IP check")
				} else {
					fmt.Printf("fetching external IPs via xray (%s)...\n\n", xrayBin)
					for _, r := range results {
						if r.Alive {
							r.ExternalIP = getExternalIPViaXray(xrayBin, r.EP.Config, 15*time.Second)
						}
					}
				}
			}

			printVlessResults(results, cmd.Bool("alive-only"))
			return nil
		},
	}
}

func parseVlessFile(r io.Reader) ([]*checkedVless, error) {
	var out []*checkedVless
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "vless://") {
			continue
		}
		label := extractFragment(line)
		cfg, err := vless.ParseConfig(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skip: %v\n", err)
			continue
		}
		out = append(out, &checkedVless{Config: cfg, Label: label})
	}
	return out, scanner.Err()
}

func extractFragment(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	label, _ := url.QueryUnescape(u.Fragment)
	return label
}

func checkVlessEndpoints(ctx context.Context, eps []*checkedVless, workers int, timeout time.Duration) []*vlessResult {
	results := make([]*vlessResult, len(eps))
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i, ep := range eps {
		results[i] = &vlessResult{EP: ep}
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			checkOne(ctx, results[idx], timeout)
		}(i)
	}
	wg.Wait()
	return results
}

func checkOne(_ context.Context, r *vlessResult, timeout time.Duration) {
	addr := r.EP.ServerAddr()

	ips, err := net.LookupHost(r.EP.Host)
	if err != nil {
		r.Err = fmt.Sprintf("dns: %v", err)
		return
	}
	r.ServerIP = ips[0]

	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		r.Err = fmt.Sprintf("tcp: %v", err)
		return
	}
	conn.Close()
	r.Latency = time.Since(start)
	r.Alive = true
}

// ─── xray external-IP ───────────────────────────────────────────────────────

func findXray() string { return vless.FindXray() }

func getExternalIPViaXray(xrayBin string, cfg *vless.Config, timeout time.Duration) string {
	port, err := freeTCPPort()
	if err != nil {
		return ""
	}

	cfgBytes, err := buildXrayConfig(cfg, port)
	if err != nil {
		return ""
	}

	tmp, err := os.CreateTemp("", "xray-*.json")
	if err != nil {
		return ""
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(cfgBytes); err != nil {
		return ""
	}
	tmp.Close()

	cmd := exec.Command(xrayBin, "run", "-c", tmp.Name())
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return ""
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	if !waitForPort(port, 5*time.Second) {
		return ""
	}

	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), nil, proxy.Direct)
	if err != nil {
		return ""
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	resp, err := client.Get("https://api.ipify.org?format=text")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port + rand.Intn(10), nil
}

func waitForPort(port int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func buildXrayConfig(cfg *vless.Config, socksPort int) ([]byte, error) {
	return vless.BuildXrayConfig(cfg, socksPort)
}

// ─── output ─────────────────────────────────────────────────────────────────

func printVlessResults(results []*vlessResult, aliveOnly bool) {
	col := func(s string, n int) string {
		if len(s) > n {
			return s[:n-1] + "…"
		}
		return s + strings.Repeat(" ", n-len(s))
	}

	header := fmt.Sprintf("%-40s  %-25s  %-16s  %-16s  %-8s  %s",
		"Label", "Server", "Server IP", "External IP", "Latency", "Status")
	sep := strings.Repeat("─", len(header))
	fmt.Println(header)
	fmt.Println(sep)

	alive, dead := 0, 0
	for _, r := range results {
		if r.Alive {
			alive++
		} else {
			dead++
		}
	}

	for _, r := range results {
		if aliveOnly && !r.Alive {
			continue
		}
		latency := "-"
		if r.Alive {
			latency = r.Latency.Round(time.Millisecond).String()
		}
		status := "DEAD"
		if r.Alive {
			status = "OK"
		}
		extIP := r.ExternalIP
		if extIP == "" {
			extIP = "-"
		}
		srvIP := r.ServerIP
		if srvIP == "" {
			srvIP = "-"
		}
		fmt.Printf("%s  %s  %s  %s  %-8s  %s\n",
			col(r.EP.Label, 40),
			col(r.EP.ServerAddr(), 25),
			col(srvIP, 16),
			col(extIP, 16),
			col(latency, 8),
			status,
		)
		if !r.Alive && r.Err != "" {
			fmt.Printf("  └─ %s\n", r.Err)
		}
	}

	fmt.Println(sep)
	fmt.Printf("total: %d  alive: %d  dead: %d\n", len(results), alive, dead)
}
