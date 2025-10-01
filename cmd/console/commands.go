package console

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"

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
					Usage:    "Path to txt file with proxies (host:port per line)",
					Required: true,
				},
				cfgPathsFlag(),
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				conf, err := loadConfig(command.Args().Slice(), command.StringSlice("configs"))
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				filePath := command.String("file")

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

				scanner := bufio.NewScanner(f)
				lineNum := 0
				for scanner.Scan() {
					lineNum++
					line := strings.TrimSpace(scanner.Text())
					if line == "" {
						continue
					}

					// ожидаем host:port
					if !strings.Contains(line, ":") {
						fmt.Printf("skip line %d: invalid format\n", lineNum)
						continue
					}

					username := randomString(8)
					password := randomString(12)

					if err := pr.AddProxy(username, password, line); err != nil {
						continue
					}

					fmt.Printf("%s:%s@%s:%s \n", username, password, conf.HTTP.Host, conf.HTTP.Port)
				}
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("failed to read file: %w", err)
				}

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
