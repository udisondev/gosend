package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/udisondev/gosend/internal/p2p"
)

func main() {
	addr := flag.String("addr", ":6789", "listen addr")
	flag.Parse()

	slog.SetLogLoggerLevel(slog.LevelDebug)
	ctx, stop := context.WithCancel(context.Background())

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sig
		stop()
	}()

	pubsign, privsign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}

	fmt.Println("My HexID:", hex.EncodeToString(pubsign))

	cli, err := p2p.NewP2PClient(ctx, *addr, pubsign, privsign)
	if err != nil {
		panic(err)
	}

	go listenPeers(ctx, cli.Listen())

	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for {
			log.Print("Введите hex: ")
			if !sc.Scan() {
				break
			}
			if err := cli.ConnectWith(sc.Text()); err != nil {
				panic(err)
			}
		}
	}()

	<-ctx.Done()
}

func listenPeers(ctx context.Context, peers <-chan *p2p.Peer) {
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-peers:
			if !ok {
				return
			}
			slog.Debug("Received new peer!")
			go listenPeer(ctx, p)
		}
	}
}

func listenPeer(ctx context.Context, p *p2p.Peer) {
	outbox := make(chan p2p.Message)
	inbox := p.Interact(outbox)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-inbox:
			if !ok {
				return
			}

			log.Printf("%s: %s", p.HexID(), msg)
		}
	}
}
