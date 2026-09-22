package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/lukelex/kmonad-device-manager/internal/manager"
	"github.com/lukelex/kmonad-device-manager/internal/platform"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), platform.Default().TerminationSignals()...)
	defer stop()
	os.Exit(manager.Run(ctx, os.Args[1:], version))
}
