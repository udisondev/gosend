package main

import (
	"context"
	"flag"
	"github.com/udisondev/gosend/router"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	slog.SetLogLoggerLevel(slog.LevelDebug)

	addr := flag.String("addr", ":6789", "listen addr")
	flag.Parse()

	ctx, stop := context.WithCancel(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sig
		stop()
	}()

	router.Run(ctx, *addr, 50000, 1024<<20)
}
