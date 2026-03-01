package chantrace

import (
	"context"
	"testing"
	"time"
)

func waitForReportCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for analyzer condition")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAnalyzerDetectsBlockedSend(t *testing.T) {
	analyzer := NewAnalyzer(
		WithAnalyzerBlockedThreshold(2 * time.Millisecond),
	)
	Enable(WithBackend(analyzer))
	t.Cleanup(Shutdown)

	ch := Make[int]("analyzer-blocked-send")
	done := make(chan struct{})

	go func() {
		defer close(done)
		Send(ch, 1)
	}()

	waitForReportCondition(t, time.Second, func() bool {
		r := analyzer.Report()
		for _, b := range r.Blocked {
			if b.Kind == ChanSendStart && b.ChannelName == "analyzer-blocked-send" {
				return true
			}
		}
		return false
	})

	if v := Recv[int](ch); v != 1 {
		t.Fatalf("Recv = %d, want 1", v)
	}
	<-done

	waitForReportCondition(t, time.Second, func() bool {
		r := analyzer.Report()
		for _, b := range r.Blocked {
			if b.ChannelName == "analyzer-blocked-send" {
				return false
			}
		}
		return true
	})
}

func TestAnalyzerDetectsLeakedGoroutine(t *testing.T) {
	analyzer := NewAnalyzer(
		WithAnalyzerLeakThreshold(2 * time.Millisecond),
	)
	Enable(WithBackend(analyzer))
	t.Cleanup(Shutdown)

	release := make(chan struct{})
	started := make(chan struct{})

	Go(context.Background(), "analyzer-leak", func(_ context.Context) {
		close(started)
		<-release
	})
	<-started

	waitForReportCondition(t, time.Second, func() bool {
		r := analyzer.Report()
		for _, g := range r.Leaked {
			if g.Label == "analyzer-leak" {
				return true
			}
		}
		return false
	})

	close(release)

	waitForReportCondition(t, time.Second, func() bool {
		r := analyzer.Report()
		for _, g := range r.Leaked {
			if g.Label == "analyzer-leak" {
				return false
			}
		}
		return true
	})
}

func TestAnalyzerTraceLostInvalidatesInflightState(t *testing.T) {
	analyzer := NewAnalyzer(WithAnalyzerBlockedThreshold(0))
	now := time.Now().Add(-time.Second).UnixNano()

	analyzer.HandleEvent(Event{
		Kind:        ChanSendStart,
		OpID:        42,
		Timestamp:   now,
		ChannelName: "op",
	})

	report := analyzer.Report()
	if len(report.Blocked) != 1 {
		t.Fatalf("blocked count = %d, want 1", len(report.Blocked))
	}

	analyzer.HandleEvent(Event{
		Kind:      TraceLost,
		Timestamp: time.Now().UnixNano(),
		Dropped:   7,
	})

	report = analyzer.Report()
	if !report.StateUncertain {
		t.Fatal("expected state_uncertain=true after TraceLost")
	}
	if report.DroppedEvents != 7 {
		t.Fatalf("dropped_events = %d, want 7", report.DroppedEvents)
	}
	if len(report.Blocked) != 0 {
		t.Fatalf("blocked count = %d, want 0 after TraceLost invalidation", len(report.Blocked))
	}
}
