package chantrace

import "testing"

func TestNextGoroutineID(t *testing.T) {
	id1 := nextGoroutineID()
	id2 := nextGoroutineID()
	if id1 <= 0 {
		t.Fatalf("nextGoroutineID() returned %d, want > 0", id1)
	}
	if id2 != id1+1 {
		t.Fatalf("nextGoroutineID() returned %d, want %d", id2, id1+1)
	}
}

func TestCurrentRuntimeGID(t *testing.T) {
	if id := currentRuntimeGID(); id <= 0 {
		t.Fatalf("currentRuntimeGID() = %d, want > 0", id)
	}
}
