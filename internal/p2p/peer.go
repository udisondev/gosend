package p2p

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"
	"github.com/udisondev/gosend/pkg/protocol"
)

type Peer struct {
	id         [protocol.ClientIDSize]byte
	mu         sync.Mutex
	isClosed   bool
	connected  chan struct{}
	inCh       chan Message
	dc         *webrtc.DataChannel
	pc         *webrtc.PeerConnection
	disconnect func()
	ctx        context.Context
}

func (p *Peer) HexID() string {
	return hex.EncodeToString(p.id[:])
}

func (p *Peer) Interact(out <-chan Message) <-chan Message {
	go func() {
		for {
			select {
			case <-p.ctx.Done():
				slog.Debug("Peer context closed", "hexID", p.HexID())
				return
			case m, ok := <-out:
				if !ok {
					return
				}

				b, err := json.Marshal(m)
				if err != nil {
					slog.Error("Failed to marshal message", "message", m, "err", err)
					continue
				}
				if err := p.dc.Send(b); err != nil {
					slog.Error("Failed to send message", "message", m, "err", err)
					continue
				}
			}
		}
	}()

	return p.inCh
}
