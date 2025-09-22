package router

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/udisondev/gosend/pkg/protocol"
)

type router struct {
	conns      map[[protocol.ClientIDSize]byte]*conn
	connsMu    sync.RWMutex
	pool       sync.Pool
	headerPool sync.Pool
	authPool   sync.Pool
	opts       routerOpts
}

type conn struct {
	sendMu       sync.Mutex
	writeTimeout time.Duration
	readTimeout  time.Duration
	conn         net.Conn
	id           [protocol.ClientIDSize]byte
	pool         *sync.Pool
	send         func([]byte) error
	disconnect   func()
}

func Run(
	ctx context.Context,
	addr string,
	opts ...RouterOpt,
) error {
	routerOpts := routerOpts{
		maxInputSize:  DefaultMaxInputSize,
		maxConnSize:   DefaultMaxConnSize,
		outboxBufSize: DefaultOutboxBufSize,
		rateLimit:     DefaultRateLimit,
		burst:         DefaultBurst,
	}
	for _, apply := range opts {
		if err := apply(&routerOpts); err != nil {
			return fmt.Errorf("apply options: %w", err)
		}
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	r := router{
		conns: make(map[[protocol.ClientIDSize]byte]*conn, routerOpts.maxConnSize),
		authPool: sync.Pool{New: func() any {
			return make([]byte, ed25519.PublicKeySize+protocol.ChallangeSize+ed25519.SignatureSize)
		}},
		pool: sync.Pool{New: func() any {
			return make([]byte, routerOpts.maxInputSize)
		}},
		opts: routerOpts,
	}

	slog.Info("Ready to listen", "addr", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("listener.Accept: %w", err)
		}

		go r.handleConn(ctx, conn)
	}
}

func (r *router) handleConn(ctx context.Context, netconn net.Conn) {
	defer netconn.Close()

	id, err := r.auth(2*time.Second, netconn)
	if err != nil {
		slog.Error("Failed to authenticate connection", "err", err, "remote", netconn.RemoteAddr())
		return
	}

	clientCtx, disconnect := context.WithCancel(ctx)
	outbox := make(chan []byte, r.opts.outboxBufSize)
	defer close(outbox)

	go func() {
		defer disconnect()

		for out := range outbox {
			if err = binary.Write(netconn, binary.BigEndian, uint32(len(out))); err != nil {
				slog.Error("Failed to send message len", "err", err, "id", hex.EncodeToString(id[:]))
				return
			}
			if _, err := netconn.Write(out); err != nil {
				slog.Error("Failed to send message", "err", err, "id", hex.EncodeToString(id[:]))
				return
			}
		}
	}()

	conn := conn{
		id: id,
		send: func(out []byte) error {
			select {
			case outbox <- out:
				return nil
			default:
				return errors.New("busy")
			}
		},
		conn:       netconn,
		pool:       &r.pool,
		disconnect: disconnect,
	}

	r.connsMu.Lock()
	r.conns[id] = &conn
	r.connsMu.Unlock()

	defer func() {
		r.connsMu.Lock()
		delete(r.conns, id)
		r.connsMu.Unlock()
		disconnect()
	}()

	for {

	}
}

func (r *router) handleMessage(sender *conn) error {
	header := r.headerPool.Get().([]byte)
	defer r.headerPool.Put(header)

	if _, err := io.ReadFull(sender.conn, header); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	inLen := binary.BigEndian.Uint32(header[:4])
	if inLen > uint32(r.opts.maxInputSize) {
		return errors.New("too big")
	}

	offset := 4
	reqID := [protocol.RequestIDSize]byte(header[offset : offset+protocol.RequestIDSize])
	offset += 4
	recipID := [protocol.ClientIDSize]byte(header[offset:])

	r.connsMu.Lock()
	recip, ok := r.conns[recipID]
	r.connsMu.Unlock()
	if !ok {
		if _, err := sender.Write(protocol.EncodeNotFoundResponse(reqID)); err != nil {
			return fmt.Errorf("send response: %w", err)
		}
		return nil
	}

	copy(header[offset:], sender.id[:])

	if _, err := recip.Write(header); err != nil {
		recip.disconnect()
		if _, err := sender.Write(protocol.EncodeSendErrorResponse(reqID)); err != nil {
			return fmt.Errorf("send response: %w", err)
		}
		return nil
	}

	if _, err := io.CopyN(recip, sender.conn, int64(inLen)-protocol.HeaderLen); err != nil {
		recip.disconnect()
		if _, err := sender.Write(protocol.EncodeSendErrorResponse(reqID)); err != nil {
			return fmt.Errorf("send response: %w", err)
		}
		return nil
	}

	return nil
}

func (r *router) auth(timeout time.Duration, conn net.Conn) (id [protocol.ClientIDSize]byte, err error) {
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})

	buf := r.authPool.Get().([]byte)
	clear(buf)
	defer r.authPool.Put(buf)

	pubsign := buf[:ed25519.PublicKeySize]
	tail := buf[ed25519.PublicKeySize:]
	challange := tail[:protocol.ChallangeSize]
	tail = tail[protocol.ChallangeSize:]
	sign := tail[:ed25519.SignatureSize]
	if _, err := io.ReadFull(conn, pubsign); err != nil {
		return id, fmt.Errorf("read pubsign timeout: %w", err)
	}

	rand.Read(challange)
	if _, err := conn.Write(challange); err != nil {
		return id, fmt.Errorf("send challange: %w", err)
	}

	if _, err := io.ReadFull(conn, sign); err != nil {
		return id, fmt.Errorf("read sign: %w", err)
	}

	if !ed25519.Verify(pubsign, challange, sign) {
		return id, errors.New("failed")
	}

	copy(id[:], pubsign)

	return id, nil
}

func (c *conn) Inbox(conn net.Conn, maxInputSize int) <-chan []byte {
	out := make(chan []byte)
	go func() {
		defer close(out)
		idStr := hex.EncodeToString(c.id[:])

		var mlen uint32
		for {
			if err := binary.Read(conn, binary.BigEndian, &mlen); err != nil {
				slog.Error("Failed to read message length", "err", err, "id", idStr)
				return
			}
			if mlen > uint32(maxInputSize) {
				return
			}

			buf := c.pool.Get().([]byte)
			buf = buf[:mlen]

			if _, err := io.ReadFull(conn, buf); err != nil {
				c.pool.Put(buf)
				slog.Error("Failed to read message", "err", err, "id", idStr)
				return
			}

			out <- buf
		}
	}()

	return out
}

func (c *conn) Write(b []byte) (int, error) {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()

	c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	defer c.conn.SetWriteDeadline(time.Time{})

	return c.conn.Write(b)
}
