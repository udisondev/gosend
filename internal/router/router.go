package router

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/udisondev/gosend/pkg/pipe"
	"github.com/udisondev/gosend/pkg/protocol"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

type router struct {
	conns           map[[protocol.ClientIDSize]byte]*client
	connsMu         sync.RWMutex
	clietsCounter   atomic.Int32
	messagesCounter atomic.Int32
	maxsize         uint32
	pool            sync.Pool
	authPool        sync.Pool
	clientPoolSize  int
}

type client struct {
	id         [protocol.ClientIDSize]byte
	pool       *sync.Pool
	send       func([]byte) error
	disconnect func()
}

func Run(ctx context.Context, addr string, maxClients int, maxInputSize uint32) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	r := router{
		conns:   make(map[[protocol.ClientIDSize]byte]*client, maxClients),
		maxsize: maxInputSize,
		authPool: sync.Pool{New: func() any {
			return make([]byte, ed25519.PublicKeySize+protocol.ChallangeSize+ed25519.SignatureSize)
		}},
		pool: sync.Pool{New: func() any {
			return make([]byte, maxInputSize)
		}},
		clientPoolSize: 10,
	}
	var wg sync.WaitGroup

	slog.Info("Ready to listen", "addr", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			break
		}
		slog.Debug("Accepted new connection", "remote", conn.RemoteAddr())

		wg.Go(func() { r.handleConn(ctx, conn) })
	}

	wg.Wait()
	slog.Info("All connections closed")

	return nil
}

func (r *router) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	id, err := r.auth(2*time.Second, conn)
	if err != nil {
		slog.Error("Failed to authenticate connection", "err", err, "remote", conn.RemoteAddr())
		return
	}
	slog.Debug("Authentication successful", "id", hex.EncodeToString(id[:]), "remote", conn.RemoteAddr())

	clientCtx, disconnect := context.WithCancel(ctx)
	outbox := make(chan []byte, r.clientPoolSize)
	defer close(outbox)

	go func() {
		defer disconnect()
		slog.Debug("Started sender goroutine", "id", hex.EncodeToString(id[:]))

		for out := range outbox {
			slog.Debug("Sending message length", "id", hex.EncodeToString(id[:]), "len", len(out))
			if err = binary.Write(conn, binary.BigEndian, uint32(len(out))); err != nil {
				slog.Error("Failed to send message len", "err", err, "id", hex.EncodeToString(id[:]))
				return
			}

			slog.Debug("Sending message", "id", hex.EncodeToString(id[:]), "len", len(out))
			if _, err := conn.Write(out); err != nil {
				slog.Error("Failed to send message", "err", err, "id", hex.EncodeToString(id[:]))
				return
			}
		}
		slog.Debug("Sender goroutine exiting", "id", hex.EncodeToString(id[:]))
	}()

	client := client{
		id: id,
		send: func(out []byte) error {
			select {
			case outbox <- out:
				slog.Debug("Queued message to send", "id", hex.EncodeToString(id[:]), "len", len(out))
				return nil
			default:
				slog.Warn("Send queue full, busy", "id", hex.EncodeToString(id[:]))
				return errors.New("busy")
			}
		},
		pool:       &r.pool,
		disconnect: disconnect,
	}

	inbox := pipe.LimitRate(client.Inbox(conn), rate.Limit(50), 100)

	r.connsMu.Lock()
	r.conns[id] = &client
	r.connsMu.Unlock()
	slog.Debug("Added client to connections", "id", hex.EncodeToString(id[:]), "total_clients", len(r.conns))

	defer func() {
		r.connsMu.Lock()
		delete(r.conns, id)
		r.connsMu.Unlock()
		slog.Debug("Removed client from connections", "id", hex.EncodeToString(id[:]), "total_clients", len(r.conns))
		disconnect()
		slog.Debug("Client disconnected", "id", hex.EncodeToString(id[:]))
	}()

	for {
		select {
		case <-clientCtx.Done():
			slog.Debug("Client context done", "id", hex.EncodeToString(id[:]))
			return
		case in, ok := <-inbox:
			if !ok {
				slog.Debug("Inbox channel closed", "id", hex.EncodeToString(id[:]))
				return
			}
			slog.Debug("Processing incoming message", "id", hex.EncodeToString(id[:]), "len", len(in))
			func() {
				defer client.pool.Put(in)

				if err := r.handleMessage(&client, in); err != nil {
					slog.Error("Failed to handle message", "err", err, "id", hex.EncodeToString(id[:]))
					return
				}
			}()
		}
	}
}

func (r *router) handleMessage(c *client, b []byte) (err error) {
	var out protocol.Outcome
	if err := out.Unmarshal(b); err != nil {
		return fmt.Errorf("unmarshal outcome: %w", err)
	}
	slog.Debug("Unmarshaled outcome", "sender_id", hex.EncodeToString(c.id[:]), "req_log_id", hex.EncodeToString(out.ReqLogID), "recip_id", hex.EncodeToString(out.RecipientID[:]), "payload_len", len(out.Payload))

	var resp []byte
	defer func() {
		if resp == nil {
			return
		}
		slog.Debug("Sending response", "sender_id", hex.EncodeToString(c.id[:]), "type", respType(resp), "len", len(resp))
		err = c.send(resp)
		if err != nil {
			slog.Warn("Failed to send response", "err", err, "sender_id", hex.EncodeToString(c.id[:]))
		}
	}()

	r.connsMu.RLock()
	defer r.connsMu.RUnlock()

	recip, ok := r.conns[out.RecipID()]
	if !ok {
		slog.Debug("Recipient not found", "recip_id", hex.EncodeToString(out.RecipientID[:]))
		resp = protocol.EncodeNotFoundResponse(out)
		return
	}

	pm := protocol.PrivateMessage(out, c.id)
	slog.Debug("Forwarding private message", "recip_id", hex.EncodeToString(out.RecipientID[:]), "len", len(pm))
	if err := recip.send(pm); err != nil {
		slog.Warn("Failed to send private message to recipient", "err", err, "recip_id", hex.EncodeToString(out.RecipientID[:]))
		resp = protocol.EncodeSendErrorResponse(out)
		recip.disconnect()
		return nil
	}

	resp = protocol.EncodeSendSuccessResponse(out)

	return
}

func respType(b []byte) string {
	if len(b) < 4 {
		return "unknown"
	}
	t := binary.BigEndian.Uint32(b[:4])
	switch t {
	case protocol.SendSuccess:
		return "SendSuccess"
	case protocol.SendError:
		return "SendError"
	case protocol.NotFound:
		return "NotFound"
	default:
		return "unknown"
	}
}

func (r *router) auth(timeout time.Duration, conn net.Conn) ([protocol.ClientIDSize]byte, error) {
	handleErr := func(desc string, err error) ([protocol.ClientIDSize]byte, error) {
		return [protocol.ClientIDSize]byte{}, fmt.Errorf("%s: %w", desc, err)
	}
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})
	slog.Debug("Starting authentication", "remote", conn.RemoteAddr())

	buf := r.authPool.Get().([]byte)
	clear(buf)
	defer r.authPool.Put(buf)

	pubsign := buf[:ed25519.PublicKeySize]
	tail := buf[ed25519.PublicKeySize:]
	challange := tail[:protocol.ChallangeSize]
	tail = tail[protocol.ChallangeSize:]
	sign := tail[:ed25519.SignatureSize]
	if _, err := io.ReadFull(conn, pubsign); err != nil {
		slog.Error("Failed to read pubsign", "err", err, "remote", conn.RemoteAddr())
		return handleErr("read pubsign timeout", err)
	}
	slog.Debug("Read pubsign", "pubsign", hex.EncodeToString(pubsign))

	rand.Read(challange)
	if _, err := conn.Write(challange); err != nil {
		slog.Error("Failed to send challenge", "err", err, "remote", conn.RemoteAddr())
		return handleErr("send challange", err)
	}
	slog.Debug("Sent challenge", "challenge", hex.EncodeToString(challange))

	if _, err := io.ReadFull(conn, sign); err != nil {
		slog.Error("Failed to read sign", "err", err, "remote", conn.RemoteAddr())
		return handleErr("read sign", err)
	}
	slog.Debug("Read sign", "sign", hex.EncodeToString(sign))

	if !ed25519.Verify(pubsign, challange, sign) {
		slog.Error("Verification failed", "remote", conn.RemoteAddr())
		return [protocol.ClientIDSize]byte{}, errors.New("failed")
	}
	slog.Debug("Verification successful", "remote", conn.RemoteAddr())

	var out [protocol.ClientIDSize]byte
	copy(out[:], pubsign)

	return out, nil
}

func (c *client) Inbox(conn net.Conn) <-chan []byte {
	out := make(chan []byte)
	go func() {
		defer close(out)
		idStr := hex.EncodeToString(c.id[:])
		slog.Debug("Started reader goroutine", "id", idStr)

		var mlen uint32
		for {
			slog.Debug("Waiting to read message length", "id", idStr)
			if err := binary.Read(conn, binary.BigEndian, &mlen); err != nil {
				slog.Error("Failed to read message length", "err", err, "id", idStr)
				return
			}
			slog.Debug("Read message length", "id", idStr, "mlen", mlen)

			buf := c.pool.Get().([]byte)
			clear(buf)

			if mlen > uint32(len(buf)) {
				c.pool.Put(buf)
				slog.Error("Too big message length", "mlen", mlen, "max", len(buf), "id", idStr)
				return
			}

			slog.Debug("Reading message body", "id", idStr, "mlen", mlen)
			if _, err := io.ReadFull(conn, buf[:mlen]); err != nil {
				c.pool.Put(buf)
				slog.Error("Failed to read message", "err", err, "id", idStr)
				return
			}
			slog.Debug("Read message body", "id", idStr, "len", mlen)

			out <- buf[:mlen]
		}
	}()

	return out
}
