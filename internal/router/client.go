package router

import (
	// "context"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udisondev/gosend/pkg/protocol"
)

type Client struct {
	id       [protocol.ClientIDSize]byte
	addr     string
	reqs     map[[protocol.RequestIDSize]byte]chan protocol.ServerMessage
	reqsMu   sync.Mutex
	conn     net.Conn
	sendMu   sync.Mutex
	isListen atomic.Bool
	isClosed atomic.Bool
	inbox    chan protocol.ServerMessage
	opts     clientOpts
}

var ErrUnmarshal = errors.New("unmarshal")

func NewClient(
	addr string,
	pubsign ed25519.PublicKey,
	privsign ed25519.PrivateKey,
	opts ...ClientOpt,
) (*Client, error) {
	clientOpt := clientOpts{
		readTimeout:  DefaultReadTimeout,
		connTimeout:  DefaultConnTimeout,
		inboxBufSize: DefaultInboxBufSize,
	}
	for _, apply := range opts {
		if err := apply(&clientOpt); err != nil {
			return nil, fmt.Errorf("apply options: %w", err)
		}
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), clientOpt.connTimeout)
	defer cancel()

	var dial net.Dialer
	conn, err := dial.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("net.Dial: %w", err)
	}

	if err := auth(conn, clientOpt.connTimeout, pubsign, privsign); err != nil {
		return nil, fmt.Errorf("authentication: %w", err)
	}

	c := Client{
		id:    [protocol.ClientIDSize]byte(pubsign),
		reqs:  make(map[[protocol.RequestIDSize]byte]chan protocol.ServerMessage, 100),
		inbox: make(chan protocol.ServerMessage, clientOpt.inboxBufSize),
		conn:  conn,
	}

	go listenRouter(conn, clientOpt.readTimeout, clientOpt.inboxBufSize)

	return &c, nil
}

func (c *Client) Inbox(ctx context.Context, bufSize int) <-chan protocol.ServerMessage {
	return c.inbox
}

func listenRouter(
	conn net.Conn,
	readTimeout time.Duration,
	bufSize int,
) <-chan protocol.ServerMessage {
	out := make(chan protocol.ServerMessage, bufSize)

	go func() {
		defer close(out)

		for {
			in, err := read(conn, readTimeout)
			if errors.Is(err, ErrUnmarshal) {
				slog.Error("Failed to unmarshal server message", "err", err)
				continue
			}
			out <- in
		}
	}()

	return out
}

func read(conn net.Conn, timeout time.Duration) (protocol.ServerMessage, error) {
	conn.SetReadDeadline(time.Now().Add(timeout))
	defer conn.SetReadDeadline(time.Time{})

	var mlen uint32
	var in protocol.ServerMessage
	if err := binary.Read(conn, binary.BigEndian, &mlen); err != nil {
		return in, fmt.Errorf("read message len: %w", err)
	}

	buf := make([]byte, mlen)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return in, fmt.Errorf("read message: %w", err)
	}

	if err := in.Unmarshal(buf); err != nil {
		return in, errors.Join(ErrUnmarshal, err)
	}

	return in, nil
}

func (c *Client) Send(o protocol.ClientMessage) (ch <-chan protocol.ServerMessage, err error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	output := o.Mashal()
	if err = binary.Write(c.conn, binary.BigEndian, uint32(len(output))); err != nil {
		return ch, fmt.Errorf("send message len: %w", err)
	}

	if _, err = c.conn.Write(output); err != nil {
		return ch, fmt.Errorf("send message: %w", err)
	}

	c.reqsMu.Lock()
	defer c.reqsMu.Unlock()

	resp := make(chan protocol.ServerMessage)
	c.reqs[o.RequestID] = resp

	return resp, nil
}

func (c *Client) ID() [protocol.ClientIDSize]byte {
	return c.id
}

func (c *Client) Cose() {
	c.conn.Close()
}

func auth(
	conn net.Conn,
	timeout time.Duration,
	pubsign ed25519.PublicKey,
	privsign ed25519.PrivateKey,
) error {
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})

	if _, err := conn.Write(pubsign); err != nil {
		return fmt.Errorf("send pubsign: %w", err)
	}

	challange := make([]byte, protocol.ChallangeSize)
	if _, err := io.ReadFull(conn, challange); err != nil {
		return fmt.Errorf("read challange: %w", err)
	}

	sign := ed25519.Sign(privsign, challange)
	if _, err := conn.Write(sign); err != nil {
		return fmt.Errorf("send sign: %w", err)
	}

	return nil
}
