package chantrace

import "testing"

func TestRegisterAndLookup(t *testing.T) {
	ch := make(chan int, 5)
	registerChan(ch, "test-ch", "int", 5)
	t.Cleanup(func() { unregisterChan(ch) })

	ptr, meta := lookupChan(ch)
	if ptr == 0 {
		t.Fatal("chanPtr returned 0")
	}
	if meta == nil {
		t.Fatal("lookupChan returned nil metadata")
	}
	if meta.Name != "test-ch" {
		t.Errorf("Name = %q, want %q", meta.Name, "test-ch")
	}
	if meta.ElemType != "int" {
		t.Errorf("ElemType = %q, want %q", meta.ElemType, "int")
	}
	if meta.Cap != 5 {
		t.Errorf("Cap = %d, want 5", meta.Cap)
	}
}

func TestLookupUnregistered(t *testing.T) {
	ch := make(chan string)
	// Ensure this address isn't a stale entry from a prior test's GC'd channel.
	unregisterChan(ch)

	ptr, meta := lookupChan(ch)
	if ptr == 0 {
		t.Fatal("chanPtr returned 0 for unregistered channel")
	}
	if meta != nil {
		t.Fatal("expected nil metadata for unregistered channel")
	}
}

func TestUnregister(t *testing.T) {
	ch := make(chan int)
	registerChan(ch, "temp", "int", 0)

	_, meta := lookupChan(ch)
	if meta == nil {
		t.Fatal("expected metadata after register")
	}

	unregisterChan(ch)

	_, meta = lookupChan(ch)
	if meta != nil {
		t.Fatal("expected nil metadata after unregister")
	}
}

func TestDirectionalChannelLookup(t *testing.T) {
	ch := make(chan int, 3)
	registerChan(ch, "bidir", "int", 3)
	t.Cleanup(func() { unregisterChan(ch) })

	// Send-only direction should resolve to same pointer
	var sendOnly chan<- int = ch
	ptr1, _ := lookupChan(ch)
	ptr2, meta := lookupChan(sendOnly)
	if ptr1 != ptr2 {
		t.Errorf("directional channel pointer mismatch: %d vs %d", ptr1, ptr2)
	}
	if meta == nil || meta.Name != "bidir" {
		t.Error("failed to look up channel via send-only reference")
	}

	// Receive-only direction
	var recvOnly <-chan int = ch
	ptr3, meta := lookupChan(recvOnly)
	if ptr1 != ptr3 {
		t.Errorf("recv-only channel pointer mismatch: %d vs %d", ptr1, ptr3)
	}
	if meta == nil || meta.Name != "bidir" {
		t.Error("failed to look up channel via recv-only reference")
	}
}

func TestUnregisterGeneric(t *testing.T) {
	ch := Make[int]("unreg-test", 1)
	_, meta := lookupChan(ch)
	if meta == nil {
		t.Fatal("expected metadata after Make")
	}

	Unregister(ch)

	_, meta = lookupChan(ch)
	if meta != nil {
		t.Fatal("expected nil metadata after Unregister")
	}

	// Channel still works after unregistering
	ch <- 42
	if v := <-ch; v != 42 {
		t.Errorf("channel broken after Unregister: got %d", v)
	}
}

func TestChannels(t *testing.T) {
	ch1 := make(chan int)
	ch2 := make(chan string, 10)
	registerChan(ch1, "ch1", "int", 0)
	registerChan(ch2, "ch2", "string", 10)
	t.Cleanup(func() {
		unregisterChan(ch1)
		unregisterChan(ch2)
	})

	infos := Channels()
	found := map[string]bool{}
	for _, info := range infos {
		found[info.Name] = true
	}
	if !found["ch1"] || !found["ch2"] {
		t.Errorf("Channels() missing registered channels: %v", infos)
	}
}
