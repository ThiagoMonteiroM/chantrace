package chantrace

import "testing"

func BenchmarkSendDisabled(b *testing.B) {
	Shutdown() // ensure disabled
	ch := make(chan int, 1)

	b.ResetTimer()
	for b.Loop() {
		Send(ch, 1)
		<-ch
	}
}

func BenchmarkSendNative(b *testing.B) {
	ch := make(chan int, 1)

	b.ResetTimer()
	for b.Loop() {
		ch <- 1
		<-ch
	}
}

func BenchmarkSendEnabled(b *testing.B) {
	rec := &recordingBackend{}
	Enable(WithBackend(rec))
	b.Cleanup(Shutdown)

	ch := Make[int]("bench", 1)

	b.ResetTimer()
	for b.Loop() {
		Send(ch, 1)
		<-ch
	}
}

func BenchmarkRecvDisabled(b *testing.B) {
	Shutdown() // ensure disabled
	ch := make(chan int, 1)

	b.ResetTimer()
	for b.Loop() {
		ch <- 1
		Recv[int](ch)
	}
}

func BenchmarkRecvEnabled(b *testing.B) {
	rec := &recordingBackend{}
	Enable(WithBackend(rec))
	b.Cleanup(Shutdown)

	ch := Make[int]("bench-recv", 1)

	b.ResetTimer()
	for b.Loop() {
		ch <- 1
		Recv[int](ch)
	}
}
