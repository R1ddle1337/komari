package jsonrpc

import (
	"context"
	"time"
)

// Historical chart requests use their own bounded pool. They must not consume
// unlimited memory/DB work or prevent independent live-status calls.
var historyQuerySlots = make(chan struct{}, 4)

func acquireHistoryQuery(parent context.Context) (context.Context, func(), error) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	select {
	case historyQuerySlots <- struct{}{}:
		return ctx, func() { <-historyQuerySlots; cancel() }, nil
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
}
