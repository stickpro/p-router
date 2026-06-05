package console

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stickpro/p-router/internal/app"
	"github.com/stickpro/p-router/internal/config"
	"github.com/stickpro/p-router/internal/repository"
	"github.com/stickpro/p-router/internal/router"
	"github.com/stickpro/p-router/pkg/cfg"
	"github.com/stickpro/p-router/pkg/logger"
	"github.com/urfave/cli/v3"
)

const (
	defaultConfigPath = "configs/config.yaml"
)

func InitCommands(currentAppVersion, appName, _ string) []*cli.Command {
	return []*cli.Command{
		vlessCheckCommand(),
		{
			Name:        "start",
			Description: "Start a proxy server",
			Flags:       []cli.Flag{cfgPathsFlag()},
			Action: func(ctx context.Context, command *cli.Command) error {
				conf, err := loadConfig(command.Args().Slice(), command.StringSlice("configs"))
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				loggerOpts := append(defaultLoggerOpts(appName, currentAppVersion), logger.WithConfig(conf.Log))

				l := logger.NewExtended(loggerOpts...)
				defer func() {
					_ = l.Sync()
				}()
				app.Run(ctx, conf, l)
				return nil
			},
		},
		{
			Name:        "import",
			Description: "Import proxies from a file",
			Flags: []cli.Flag{
				&cli.StringFlag{
					Name:     "file",
					Usage:    "Path to txt file with proxies. Formats: host:port (HTTP), socks5://host:port, socks5://user:pass@host:port",
					Required: true,
				},
				&cli.BoolFlag{
					Name:  "detect",
					Usage: "Auto-detect proxy protocol (HTTP/SOCKS5) for plain host:port entries before importing",
				},
				&cli.IntFlag{
					Name:  "workers",
					Usage: "Number of parallel workers for protocol detection",
					Value: 20,
				},
				cfgPathsFlag(),
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				conf, err := loadConfig(command.Args().Slice(), command.StringSlice("configs"))
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				filePath := command.String("file")
				detect := command.Bool("detect")
				workers := command.Int("workers")

				f, err := os.Open(filePath)
				if err != nil {
					return fmt.Errorf("failed to open file: %w", err)
				}
				defer f.Close()

				repo, err := repository.NewSQLiteRepository("proxies.db")
				if err != nil {
					log.Fatalf("Failed to create repository: %v", err)
				}
				defer repo.Close()

				pr := router.NewProxyRouter(repo)

				// Collect lines first
				var lines []string
				scanner := bufio.NewScanner(f)
				lineNum := 0
				for scanner.Scan() {
					lineNum++
					line := strings.TrimSpace(scanner.Text())
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					if !strings.Contains(line, ":") {
						fmt.Printf("skip line %d: invalid format\n", lineNum)
						continue
					}
					lines = append(lines, line)
				}
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}

				if !detect {
					// No detection: import as-is
					for _, line := range lines {
						username := randomString(8)
						password := randomString(12)
						if err := pr.AddProxy(username, password, line); err != nil {
							continue
						}
						fmt.Printf("%s:%s@%s:%s\n", username, password, conf.HTTP.Host, conf.HTTP.Port)
					}
					return nil
				}

				// Detect protocol in parallel
				type detectedProxy struct {
					target   string
					protocol string
				}

				results := make([]detectedProxy, 0, len(lines))
				resultMu := sync.Mutex{}

				sem := make(chan struct{}, workers)
				var wg sync.WaitGroup

				fmt.Printf("detecting protocol for %d proxies (workers=%d)...\n", len(lines), workers)

				for _, line := range lines {
					// If protocol already specified — skip detection
					if strings.HasPrefix(line, "socks5://") || strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "vless://") {
						resultMu.Lock()
						results = append(results, detectedProxy{target: line, protocol: "known"})
						resultMu.Unlock()
						continue
					}

					wg.Add(1)
					sem <- struct{}{}
					go func(addr string) {
						defer wg.Done()
						defer func() { <-sem }()

						protocol, err := detectProxyProtocol(addr, 5*time.Second)
						if err != nil {
							fmt.Printf("  skip %s: %v\n", addr, err)
							return
						}

						target := addr
						if protocol == "socks5" {
							target = "socks5://" + addr
						}

						resultMu.Lock()
						results = append(results, detectedProxy{target: target, protocol: protocol})
						resultMu.Unlock()

						fmt.Printf("  detected %s -> %s\n", addr, protocol)
					}(line)
				}

				wg.Wait()

				// Import detected proxies
				fmt.Printf("\nimporting %d working proxies...\n", len(results))
				for _, r := range results {
					username := randomString(8)
					password := randomString(12)
					if err := pr.AddProxy(username, password, r.target); err != nil {
						continue
					}
					fmt.Printf("%s:%s@%s:%s\n", username, password, conf.HTTP.Host, conf.HTTP.Port)
				}

				return nil
			},
		},
		{
			Name:        "redetect",
			Description: "Re-detect protocol for existing proxies in DB that have no protocol prefix (host:port)",
			Flags: []cli.Flag{
				&cli.IntFlag{
					Name:  "workers",
					Usage: "Number of parallel workers",
					Value: 20,
				},
				cfgPathsFlag(),
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				workers := command.Int("workers")

				repo, err := repository.NewSQLiteRepository("proxies.db")
				if err != nil {
					log.Fatalf("Failed to create repository: %v", err)
				}
				defer repo.Close()

				pr := router.NewProxyRouter(repo)

				all, err := pr.GetAllProxies()
				if err != nil {
					return fmt.Errorf("failed to list proxies: %w", err)
				}

				var candidates []*router.ProxyConfig
				for _, p := range all {
					if !strings.HasPrefix(p.Target, "socks5://") && !strings.HasPrefix(p.Target, "http://") {
						candidates = append(candidates, p)
					}
				}

				if len(candidates) == 0 {
					fmt.Println("no proxies to redetect")
					return nil
				}

				fmt.Printf("redetecting %d proxies (workers=%d)...\n", len(candidates), workers)

				sem := make(chan struct{}, workers)
				var wg sync.WaitGroup

				for _, p := range candidates {
					wg.Add(1)
					sem <- struct{}{}
					go func(cfg *router.ProxyConfig) {
						defer wg.Done()
						defer func() { <-sem }()

						protocol, err := detectProxyProtocol(cfg.Target, 5*time.Second)
						if err != nil {
							fmt.Printf("  dead   %s (%s): %v\n", cfg.Target, cfg.Username, err)
							return
						}

						if protocol == "http" {
							fmt.Printf("  http   %s (%s) — no change\n", cfg.Target, cfg.Username)
							return
						}

						// SOCKS5: update target in DB
						newTarget := "socks5://" + cfg.Target
						if err := pr.UpdateProxy(cfg.Username, cfg.Password, newTarget); err != nil {
							fmt.Printf("  error  %s (%s): %v\n", cfg.Target, cfg.Username, err)
							return
						}

						fmt.Printf("  socks5 %s -> %s (%s)\n", cfg.Target, newTarget, cfg.Username)
					}(p)
				}

				wg.Wait()
				fmt.Println("done")
				return nil
			},
		},
		{
			Name:        "proxy-list",
			Description: "List all proxies",
			Flags:       []cli.Flag{cfgPathsFlag()},
			Action: func(ctx context.Context, command *cli.Command) error {
				conf, err := loadConfig(command.Args().Slice(), command.StringSlice("configs"))

				repo, err := repository.NewSQLiteRepository("proxies.db")
				if err != nil {
					log.Fatalf("Failed to create repository: %v", err)
				}
				defer repo.Close()

				pr := router.NewProxyRouter(repo)
				list, _ := pr.GetAllProxies()
				for _, prx := range list {
					fmt.Printf("%s:%s@%s:%d\n", prx.Username, prx.Password, conf.HTTP.Host, conf.HTTP.Port)
				}
				return nil
			},
		},
	}
}

func detectProxyProtocol(addr string, timeout time.Duration) (string, error) {
	// --- Try SOCKS5 ---
	// Send: VER=5, NMETHODS=1, METHOD=NO_AUTH(0)
	// Expect: VER=5, METHOD=<any> (first byte must be 0x05)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", fmt.Errorf("connection failed: %w", err)
	}
	conn.SetDeadline(time.Now().Add(timeout))
	_, _ = conn.Write([]byte{0x05, 0x01, 0x00})

	buf := make([]byte, 2)
	_, err = io.ReadFull(conn, buf)
	conn.Close()

	if err == nil && buf[0] == 0x05 {
		return "socks5", nil
	}

	// --- Try HTTP CONNECT ---
	conn2, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", fmt.Errorf("connection failed: %w", err)
	}
	conn2.SetDeadline(time.Now().Add(timeout))
	_, _ = conn2.Write([]byte("CONNECT 1.1.1.1:80 HTTP/1.1\r\nHost: 1.1.1.1:80\r\n\r\n"))

	peek := make([]byte, 7)
	_, err = io.ReadFull(conn2, peek)
	conn2.Close()

	if err == nil && string(peek) == "HTTP/1." {
		return "http", nil
	}

	return "", fmt.Errorf("unknown protocol")
}

func cfgPathsFlag() *cli.StringSliceFlag {
	return &cli.StringSliceFlag{
		Name:    "configs",
		Aliases: []string{"c"},
		Usage:   "allows you to use your own paths to configuration files, separated by commas (config.yaml,config.prod.yml,.env)",
		Value:   cli.NewStringSlice(defaultConfigPath).Value(),
	}
}

func loadConfig(args, configPaths []string) (*config.Config, error) {
	conf := new(config.Config)
	if err := cfg.Load(conf,
		cfg.WithLoaderConfig(cfg.Config{
			Args:       args,
			Files:      configPaths,
			MergeFiles: true,
		}),
	); err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	return conf, nil
}

func defaultLoggerOpts(appName, version string) []logger.Option {
	return []logger.Option{
		logger.WithAppName(appName),
		logger.WithAppVersion(version),
	}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
