package p2p

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"

	"github.com/udisondev/gosend/internal/router"
)

type Client2 struct {
	mu      sync.Mutex
	rc      *router.Client
	peersCh chan *Peer
}

func NewClient(
	ctx context.Context,
	addr string,
	pubsign ed25519.PublicKey,
	privsign ed25519.PrivateKey,
) (*Client2, error) {
	rc, err := router.NewClient(ctx, addr, pubsign, privsign)
	if err != nil {
		return nil, fmt.Errorf("router.Connect: %w", err)
	}

	return &Client2{
		rc: rc,
	}, nil
}

func listenRouter(ctx context.Context)

func (c *Client2) PeersChan() <-chan *Peer {
	return c.peersCh
}
