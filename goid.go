package chantrace

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"sync/atomic"
)

var goroutineSeq atomic.Int64

func nextGoroutineID() int64 {
	return goroutineSeq.Add(1)
}

type goidKeyType struct{}

var goidKey goidKeyType

// GoID returns the goroutine ID from the context, or 0 if not set by [Go].
func GoID(ctx context.Context) int64 {
	if id, ok := ctx.Value(goidKey).(int64); ok {
		return id
	}
	return 0
}

// currentRuntimeGID parses the goroutine ID from runtime.Stack output.
// Best-effort: returns 0 if the header format changes.
func currentRuntimeGID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	if n <= 0 {
		return 0
	}

	line := buf[:n]
	const prefix = "goroutine "
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return 0
	}

	line = line[len(prefix):]
	space := bytes.IndexByte(line, ' ')
	if space < 0 {
		return 0
	}

	id, err := strconv.ParseInt(string(line[:space]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
