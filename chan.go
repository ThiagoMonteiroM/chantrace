package chantrace

import (
	"fmt"
	"iter"
	"path"
	"reflect"
	"runtime"
	"time"
	"unicode/utf8"
)

const maxValueRunes = 64

func capturePC(skip int) uintptr {
	var pcs [1]uintptr
	runtime.Callers(skip, pcs[:])
	return pcs[0]
}

// skip=4: runtime.Callers, capturePC, maybeCapturePC, Send/Recv/etc.
func maybeCapturePC() uintptr {
	if !pcCapture.Load() {
		return 0
	}
	every := pcSampleEvery.Load()
	if every <= 1 {
		return capturePC(4)
	}
	seq := pcSampleSeq.Add(1)
	if (seq-1)%uint64(every) != 0 {
		return 0
	}
	return capturePC(4)
}

func resolvePC(pc uintptr) (string, int) {
	if pc == 0 {
		return "???", 0
	}
	frames := runtime.CallersFrames([]uintptr{pc})
	f, _ := frames.Next()
	if f.File == "" {
		return "???", 0
	}
	return path.Base(f.File), f.Line
}

func truncate(s string, max int) string {
	if max < 3 {
		max = 3
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-3]) + "..."
}

func chanInfo[T any](ch any) (uintptr, string, string) {
	ptr, meta := lookupChan(ch)
	name := ""
	valType := ""
	if meta != nil {
		name = meta.Name
		valType = meta.ElemType
	}
	if valType == "" {
		valType = reflect.TypeFor[T]().String()
	}
	return ptr, name, valType
}

func captureValue(val any) string {
	if !snapshotValues.Load() {
		return ""
	}
	return truncate(fmt.Sprintf("%v", val), maxValueRunes)
}

// Make creates and registers a traced channel. The optional size sets buffer capacity.
func Make[T any](name string, size ...int) chan T {
	if len(size) > 1 {
		panic("chantrace.Make: too many arguments")
	}
	c := 0
	if len(size) == 1 {
		c = size[0]
	}
	ch := make(chan T, c)
	elemType := reflect.TypeFor[T]().String()
	ptr := registerChan(ch, name, elemType, c)

	if enabled.Load() {
		gid := currentRuntimeGID()
		defaultCollector.emit(Event{
			Kind:        ChanMake,
			Timestamp:   time.Now().UnixNano(),
			GoroutineID: gid,
			ChannelID:   ptr,
			ChannelName: name,
			ValueType:   elemType,
			BufCap:      c,
			PC:          maybeCapturePC(),
		})
	}
	return ch
}

// Register adds an existing channel to the trace registry without closing it.
func Register[T any](ch chan T, name string) {
	elemType := reflect.TypeFor[T]().String()
	ptr := registerChan(ch, name, elemType, cap(ch))

	if enabled.Load() {
		gid := currentRuntimeGID()
		defaultCollector.emit(Event{
			Kind:        ChanRegister,
			Timestamp:   time.Now().UnixNano(),
			GoroutineID: gid,
			ChannelID:   ptr,
			ChannelName: name,
			ValueType:   elemType,
			BufCap:      cap(ch),
			BufLen:      len(ch),
			PC:          maybeCapturePC(),
		})
	}
}

// Send performs a traced send. Emits ChanSendStart before and ChanSendDone after.
func Send[T any](ch chan<- T, val T) {
	if !enabled.Load() {
		ch <- val
		return
	}

	ptr, name, valType := chanInfo[T](ch)
	pc := maybeCapturePC()
	opID := nextOpID()
	gid := currentRuntimeGID()

	defaultCollector.emit(Event{
		Kind:        ChanSendStart,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		ValueStr:    captureValue(val),
		PC:          pc,
	})

	ch <- val

	defaultCollector.emit(Event{
		Kind:        ChanSendDone,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		BufLen:      len(ch),
		PC:          pc,
	})
}

// Recv performs a traced receive. Emits ChanRecvStart before and ChanRecvDone after.
func Recv[T any](ch <-chan T) T {
	if !enabled.Load() {
		return <-ch
	}

	ptr, name, valType := chanInfo[T](ch)
	pc := maybeCapturePC()
	opID := nextOpID()
	gid := currentRuntimeGID()

	defaultCollector.emit(Event{
		Kind:        ChanRecvStart,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		PC:          pc,
	})

	val := <-ch

	defaultCollector.emit(Event{
		Kind:        ChanRecvDone,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		ValueStr:    captureValue(val),
		BufLen:      len(ch),
		PC:          pc,
	})

	return val
}

// RecvOk performs a traced receive that also returns whether the channel is open.
func RecvOk[T any](ch <-chan T) (T, bool) {
	if !enabled.Load() {
		val, ok := <-ch
		return val, ok
	}

	ptr, name, valType := chanInfo[T](ch)
	pc := maybeCapturePC()
	opID := nextOpID()
	gid := currentRuntimeGID()

	defaultCollector.emit(Event{
		Kind:        ChanRecvStart,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		PC:          pc,
	})

	val, ok := <-ch

	defaultCollector.emit(Event{
		Kind:        ChanRecvDone,
		OpID:        opID,
		Timestamp:   time.Now().UnixNano(),
		GoroutineID: gid,
		ChannelID:   ptr,
		ChannelName: name,
		ValueType:   valType,
		ValueStr:    captureValue(val),
		BufLen:      len(ch),
		RecvOK:      ok,
		PC:          pc,
	})

	return val, ok
}

// Close performs a traced close and removes the channel from the registry.
func Close[T any](ch chan T) {
	ptr, name, valType := chanInfo[T](ch)
	tracing := enabled.Load()
	var gid int64
	var pc uintptr
	bufLen := len(ch)
	bufCap := cap(ch)
	if tracing {
		gid = currentRuntimeGID()
		pc = maybeCapturePC()
	}

	close(ch)
	unregisterChan(ch)

	if tracing {
		defaultCollector.emit(Event{
			Kind:        ChanClose,
			Timestamp:   time.Now().UnixNano(),
			GoroutineID: gid,
			ChannelID:   ptr,
			ChannelName: name,
			ValueType:   valType,
			BufLen:      bufLen,
			BufCap:      bufCap,
			PC:          pc,
		})
	}
}

// Range returns an iterator that performs traced receives over the channel.
func Range[T any](ch <-chan T) iter.Seq[T] {
	ptr, name, valType := chanInfo[T](ch)
	pc := maybeCapturePC()

	return func(yield func(T) bool) {
		gid := currentRuntimeGID()
		for {
			if enabled.Load() {
				defaultCollector.emit(Event{
					Kind:        ChanRangeStart,
					Timestamp:   time.Now().UnixNano(),
					GoroutineID: gid,
					ChannelID:   ptr,
					ChannelName: name,
					ValueType:   valType,
					PC:          pc,
				})
			}

			val, ok := <-ch
			if !ok {
				if enabled.Load() {
					defaultCollector.emit(Event{
						Kind:        ChanRangeDone,
						Timestamp:   time.Now().UnixNano(),
						GoroutineID: gid,
						ChannelID:   ptr,
						ChannelName: name,
						ValueType:   valType,
						PC:          pc,
					})
				}
				return
			}

			if enabled.Load() {
				defaultCollector.emit(Event{
					Kind:        ChanRange,
					Timestamp:   time.Now().UnixNano(),
					GoroutineID: gid,
					ChannelID:   ptr,
					ChannelName: name,
					ValueType:   valType,
					ValueStr:    captureValue(val),
					PC:          pc,
				})
			}
			if !yield(val) {
				return
			}
		}
	}
}
