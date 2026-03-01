package chantrace

import (
	"context"
	"sync/atomic"
)

// goroutineID is an incrementing counter assigned by chantrace.Go().
// This avoids parsing runtime.Stack (which costs ~1.7µs per call).
// Goroutines not spawned via Go() get ID 0 — use context-based
// tracing for correlation in those cases.
var goroutineSeq atomic.Int64

func nextGoroutineID() int64 {
	return goroutineSeq.Add(1)
}

// goidKeyType is the context key for chantrace goroutine IDs.
type goidKeyType struct{}

var goidKey goidKeyType

// GoID returns the chantrace goroutine ID from the context, or 0 if
// the goroutine was not spawned via chantrace.Go.
func GoID(ctx context.Context) int64 {
	if id, ok := ctx.Value(goidKey).(int64); ok {
		return id
	}
	return 0
}
