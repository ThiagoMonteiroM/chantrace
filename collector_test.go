package chantrace

import (
	"sync"
	"testing"
	"time"
)

type recordingBackend struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordingBackend) HandleEvent(e Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recordingBackend) Close() error { return nil }

func (r *recordingBackend) getEvents() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]Event, len(r.events))
	copy(cp, r.events)
	return cp
}

// waitForEvents polls the recording backend until at least n events
// have been delivered or the timeout expires.
func waitForEvents(rec *recordingBackend, n int, timeout time.Duration) []Event {
	deadline := time.After(timeout)
	for {
		events := rec.getEvents()
		if len(events) >= n {
			return events
		}
		select {
		case <-deadline:
			return events
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestCollectorEmit(t *testing.T) {
	c := &collector{ring: make([]Event, ringSize)}
	rec := &recordingBackend{}
	c.addBackend(rec)
	c.start()

	c.emit(Event{Kind: ChanSendStart, ChannelName: "test"})

	events := waitForEvents(rec, 1, time.Second)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].ChannelName != "test" {
		t.Errorf("ChannelName = %q, want %q", events[0].ChannelName, "test")
	}

	c.closeBackends()
}

func TestCollectorRingBuffer(t *testing.T) {
	c := &collector{ring: make([]Event, 4)} // small ring for testing

	for i := range 10 {
		c.emit(Event{Kind: ChanSendStart, ChannelName: "test", Line: i})
	}

	snap := c.snapshot(4)
	if len(snap) != 4 {
		t.Fatalf("snapshot len = %d, want 4", len(snap))
	}
	// Should contain events 6-9 (last 4)
	for i, e := range snap {
		want := i + 6
		if e.Line != want {
			t.Errorf("snap[%d].Line = %d, want %d", i, e.Line, want)
		}
	}
}

func TestCollectorSnapshotEmpty(t *testing.T) {
	c := &collector{ring: make([]Event, ringSize)}
	snap := c.snapshot(10)
	if snap != nil {
		t.Errorf("expected nil for empty collector, got %v", snap)
	}
}

func TestCollectorSnapshotFewerThanN(t *testing.T) {
	c := &collector{ring: make([]Event, ringSize)}
	c.emit(Event{Kind: ChanMake, ChannelName: "a"})
	c.emit(Event{Kind: ChanMake, ChannelName: "b"})

	snap := c.snapshot(100)
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
}

func TestCollectorCloseBackends(t *testing.T) {
	c := &collector{ring: make([]Event, ringSize)}
	rec := &recordingBackend{}
	c.addBackend(rec)
	c.start()

	c.emit(Event{Kind: ChanSendStart})

	events := waitForEvents(rec, 1, time.Second)
	if len(events) != 1 {
		t.Fatal("expected 1 event before close")
	}

	c.closeBackends()

	// After closing, emitting should not dispatch to backend
	c.emit(Event{Kind: ChanRecvStart})
	time.Sleep(10 * time.Millisecond)
	if len(rec.getEvents()) != 1 {
		t.Error("backend received events after close")
	}
}

// panicBackend panics on every HandleEvent call.
type panicBackend struct{}

func (p *panicBackend) HandleEvent(Event) { panic("boom") }
func (p *panicBackend) Close() error      { return nil }

func TestCollectorBackendPanicRecovery(t *testing.T) {
	c := &collector{ring: make([]Event, ringSize)}
	bad := &panicBackend{}
	rec := &recordingBackend{}
	// bad backend first, then recording backend
	c.addBackend(bad)
	c.addBackend(rec)
	c.start()

	c.emit(Event{Kind: ChanSendStart, ChannelName: "panic-test"})
	c.emit(Event{Kind: ChanSendDone, ChannelName: "panic-test"})

	// The recording backend should still receive events despite the panicking backend
	events := waitForEvents(rec, 2, time.Second)
	if len(events) < 2 {
		t.Fatalf("expected 2 events (panicking backend should not kill drain), got %d", len(events))
	}

	c.closeBackends()
}

func TestCollectorAsyncDropsOnFull(t *testing.T) {
	c := &collector{
		ring:    make([]Event, ringSize),
		bufSize: 2, // tiny buffer
	}
	rec := &recordingBackend{}
	c.addBackend(rec)
	c.start()

	// Emit more events than the buffer can hold
	for i := range 100 {
		c.emit(Event{Kind: ChanSendStart, Line: i})
	}

	// Wait for drain to process what it can
	time.Sleep(50 * time.Millisecond)
	c.closeBackends()

	// Ring buffer has all 100 events
	snap := c.snapshot(100)
	if len(snap) != 100 {
		t.Errorf("ring snapshot = %d, want 100", len(snap))
	}

	// Backend may have fewer (some dropped from dispatch)
	got := len(rec.getEvents())
	if got > 100 {
		t.Errorf("backend got %d events, expected <= 100", got)
	}
}
