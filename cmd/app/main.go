package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/stickpro/p-router/cmd/console"
	"github.com/urfave/cli/v3"
)

var (
	appName    = "p-proxy"
	version    = "local"
	commitHash = "unknown"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), shutdown()...)
	defer cancel()

	app := &cli.Command{
		Name:        appName,
		Description: "Api for go store",
		Version:     getBuildVersion(),
		Suggest:     true,
		Flags: []cli.Flag{
			cli.HelpFlag,
			cli.VersionFlag,
		},
		Commands: console.InitCommands(version, appName, commitHash),
	}

	if err := app.Run(ctx, os.Args); err != nil {
		fmt.Println(err.Error())
	}
}

func getBuildVersion() string {
	return fmt.Sprintf(
		"\n\nrelease: %s\ncommit hash: %s\ngo version: %s",
		version,
		commitHash,
		runtime.Version(),
	)
}

func shutdown() []os.Signal {
	return []os.Signal{
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGKILL,
	}
}
