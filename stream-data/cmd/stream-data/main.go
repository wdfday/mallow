package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"stream-data/internal/app"
)

const lockPort = ":17610"

func main() {
	app.InitLogger()

	// Single-instance lock: bind a TCP port so a second process fails fast.
	ln, err := net.Listen("tcp", lockPort)
	if err != nil {
		slog.Error("another instance is already running", "port", lockPort)
		os.Exit(1)
	}
	defer ln.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app.Run(ctx)
}
