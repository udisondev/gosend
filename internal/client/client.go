package client

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/udisondev/gosend/pkg/protocol"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
)

type Client struct {
	id       [protocol.ClientIDSize]byte
	reqs     map[string]chan protocol.Income
	reqsMu   sync.Mutex
	conn     net.Conn
	sendMu   sync.Mutex
	isListen atomic.Bool
	isClosed atomic.Bool
	inbox    chan protocol.Income
	close    func()
}

func Connect(
	ctx context.Context,
	addr string,
	pubsign ed25519.PublicKey,
	privsign ed25519.PrivateKey,
) (*Client, error) {
	var dial net.Dialer
	conn, err := dial.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("net.Dial: %w", err)
	}

	defer func() {
		if err == nil {
			return
		}
		conn.Close()
	}()

	if _, err = conn.Write(pubsign); err != nil {
		return nil, fmt.Errorf("send pubsign: %w", err)
	}

	challange := make([]byte, protocol.ChallangeSize)
	if _, err := io.ReadFull(conn, challange); err != nil {
		return nil, fmt.Errorf("read challange: %w", err)
	}

	sign := ed25519.Sign(privsign, challange)
	if _, err := conn.Write(sign); err != nil {
		return nil, fmt.Errorf("send sign: %w", err)
	}

	c := Client{
		id:   [protocol.ClientIDSize]byte(pubsign),
		reqs: map[string]chan protocol.Income{},
		conn: conn,
	}

	c.close = sync.OnceFunc(func() {
		c.reqsMu.Lock()
		defer c.reqsMu.Unlock()

		c.sendMu.Lock()
		defer c.sendMu.Unlock()

		for _, ch := range c.reqs {
			if ch == nil {
				continue
			}
			close(ch)
		}

		c.isClosed.Swap(true)
		c.isListen.Swap(false)

		if c.inbox != nil {
			close(c.inbox)
		}

		c.conn.Close()
	})

	go func() {
		defer c.Close()

		for {
			if err := c.read(); err != nil {
				slog.Error("Failed to read message", "err", err)
				return
			}
		}
	}()

	return &c, nil
}

func (c *Client) Listen(bufSize int) <-chan protocol.Income {
	c.inbox = make(chan protocol.Income, bufSize)
	c.isListen.Swap(true)

	return c.inbox
}

func (c *Client) Mute() {
	c.isListen.Swap(false)
}

func (c *Client) read() error {
	var mlen uint32
	if err := binary.Read(c.conn, binary.BigEndian, &mlen); err != nil {
		return fmt.Errorf("read message len: %w", err)
	}

	if !c.isListen.Load() {
		if _, err := io.CopyN(io.Discard, c.conn, int64(mlen)); err != nil {
			return fmt.Errorf("read message: %w", err)
		}
		return nil
	}

	buf := make([]byte, mlen)
	if _, err := io.ReadFull(c.conn, buf); err != nil {
		return fmt.Errorf("read message: %w", err)
	}

	var in protocol.Income
	if err := in.Unmarshal(buf); err != nil {
		return fmt.Errorf("unmarshal income: %w", err)
	}

	c.reqsMu.Lock()
	defer c.reqsMu.Unlock()
	reqLogID := in.HexReqLogID()
	if reply, ok := c.reqs[reqLogID]; ok {
		defer close(reply)
		defer delete(c.reqs, reqLogID)

		reply <- in
	} else {
		c.inbox <- in
	}

	return nil
}

func (c *Client) Send(o protocol.Outcome) (<-chan protocol.Income, error) {
	handleErr := func(desc string, err error) (<-chan protocol.Income, error) {
		return nil, fmt.Errorf("%s: %w", desc, err)
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	if c.isClosed.Load() {
		return nil, errors.New("closed")
	}

	b, err := o.Mashal()
	if err != nil {
		return handleErr("marshal outcome", err)
	}

	if err := binary.Write(c.conn, binary.BigEndian, uint32(len(b))); err != nil {
		return handleErr("send message len", err)
	}

	if _, err := c.conn.Write(b); err != nil {
		return handleErr("send message", err)
	}

	c.reqsMu.Lock()
	defer c.reqsMu.Unlock()

	resp := make(chan protocol.Income)
	c.reqs[o.HexReqLogID()] = resp

	return resp, nil
}

func (c *Client) ID() [protocol.ClientIDSize]byte {
	return c.id
}

func (c *Client) Close() {
	c.close()
}
