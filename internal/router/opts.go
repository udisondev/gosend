package router

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/udisondev/gosend/pkg/protocol"
)

type clientOpts struct {
	readTimeout  time.Duration
	connTimeout  time.Duration
	inboxBufSize int
}

type ClientOpt func(*clientOpts) error

type routerOpts struct {
	maxInputSize  int
	maxConnSize   int
	outboxBufSize int
	rateLimit     int
	burst         int
}

type RouterOpt func(*routerOpts) error

const (
	// Router
	DefaultMaxInputSize  int = math.MaxUint16
	DefaultMaxConnSize   int = 1000
	DefaultOutboxBufSize int = 10
	DefaultRateLimit     int = 50
	DefaultBurst         int = 200

	// Client
	DefaultReadTimeout  time.Duration = 3 * time.Second
	DefaultConnTimeout  time.Duration = 3 * time.Second
	DefaultInboxBufSize               = 100
)

func WithReadTimeout(d time.Duration) ClientOpt {
	return func(o *clientOpts) error {
		if d < time.Millisecond {
			return errors.New("read timeout must not be less than millisecond")
		}
		o.readTimeout = d
		return nil
	}
}

func WithConnTimeout(d time.Duration) ClientOpt {
	return func(o *clientOpts) error {
		if d < time.Millisecond {
			return errors.New("connection timeout must not be less than millisecond")
		}
		o.connTimeout = d
		return nil
	}
}

func WithInboxBufferSize(size int) ClientOpt {
	return func(o *clientOpts) error {
		if size < 0 {
			return errors.New("inbox buffer size must be greater than 0")
		}
		o.inboxBufSize = size
		return nil
	}
}
func WithMaxInputSize(size int) RouterOpt {
	return func(ro *routerOpts) error {
		if size < protocol.MinClientMessageLen {
			return fmt.Errorf("max input size must not be less than %d", protocol.MinClientMessageLen)
		}
		ro.maxInputSize = size
		return nil
	}
}

func WithMaxConnSize(size int) RouterOpt {
	return func(ro *routerOpts) error {
		ro.maxConnSize = size

		return nil
	}
}
