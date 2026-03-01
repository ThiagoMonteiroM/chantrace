package chantrace

import (
	"context"
	"time"
)

// Go launches a traced goroutine. The child receives a context with its own
// goroutine ID (see [GoID]), and its parent ID is recorded for spawn trees.
func Go(ctx context.Context, label string, fn func(ctx context.Context)) {
	if !enabled.Load() {
		go fn(ctx)
		return
	}

	parentGID := currentRuntimeGID()
	if parentGID == 0 {
		parentGID = GoID(ctx)
	}
	pc := maybeCapturePC()
	childTraceID := nextGoroutineID()
	childCtx := context.WithValue(ctx, goidKey, childTraceID)

	go func() {
		childGID := currentRuntimeGID()
		if childGID == 0 {
			childGID = childTraceID
		}
		defaultCollector.emit(Event{
			Kind:        GoSpawn,
			Timestamp:   time.Now().UnixNano(),
			GoroutineID: childGID,
			ParentGID:   parentGID,
			GoLabel:     label,
			PC:          pc,
		})

		defer func() {
			if enabled.Load() {
				defaultCollector.emit(Event{
					Kind:        GoExit,
					Timestamp:   time.Now().UnixNano(),
					GoroutineID: childGID,
					GoLabel:     label,
					PC:          pc,
				})
			}
		}()

		fn(childCtx)
	}()
}
