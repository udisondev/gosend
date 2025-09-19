package pipe

import (
	"context"

	"golang.org/x/time/rate"
)

func LimitRate(in <-chan []byte, r rate.Limit, burst int) <-chan []byte {
	out := make(chan []byte)
	limiter := rate.NewLimiter(r, burst)

	go func() {
		defer close(out)

		for val := range in {
			_ = limiter.Wait(context.Background())
			select {
			case out <- val:
			default:
				return
			}
		}
	}()

	return out
}
