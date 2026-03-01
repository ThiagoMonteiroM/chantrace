package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/khzaw/chantrace"
	_ "github.com/khzaw/chantrace/backend/web"
)

type Job struct {
	ID      int
	Payload string
	Created time.Time
}

type Parsed struct {
	JobID      int
	Worker     int
	LatencyMS  int64
	PayloadLen int
}

type Alert struct {
	JobID  int
	Level  string
	Reason string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	chantrace.Enable(
		chantrace.WithWeb(":4884"),
		chantrace.WithBufferSize(32768),
		chantrace.WithPCCapture(false),
	)
	defer chantrace.Shutdown()

	fmt.Println("chantrace web demo running")
	fmt.Println("open: http://localhost:4884/")
	fmt.Println("events: http://localhost:4884/events")
	fmt.Println("press Ctrl+C to stop")

	jobs := chantrace.Make[Job]("jobs", 64)
	parsed := chantrace.Make[Parsed]("parsed", 64)
	alerts := chantrace.Make[Alert]("alerts", 32)
	done := chantrace.Make[struct{}]("done")

	var created atomic.Int64
	var handled atomic.Int64
	var parsedCount atomic.Int64
	var alertCount atomic.Int64

	chantrace.Go(ctx, "producer", func(context.Context) {
		ticker := time.NewTicker(120 * time.Millisecond)
		defer ticker.Stop()

		nextID := 1
		for {
			select {
			case <-ctx.Done():
				chantrace.Close(jobs)
				return
			case ts := <-ticker.C:
				job := Job{
					ID:      nextID,
					Payload: fmt.Sprintf("payload-%03d", nextID),
					Created: ts,
				}
				chantrace.Send(jobs, job)
				created.Add(1)
				nextID++
			}
		}
	})

	const workerCount = 3
	var workerWG sync.WaitGroup
	for workerID := 0; workerID < workerCount; workerID++ {
		id := workerID
		workerWG.Add(1)
		chantrace.Go(ctx, fmt.Sprintf("worker-%d", id), func(context.Context) {
			defer workerWG.Done()
			for job := range chantrace.Range(jobs) {
				work := time.Duration(40+id*20) * time.Millisecond
				time.Sleep(work)

				p := Parsed{
					JobID:      job.ID,
					Worker:     id,
					LatencyMS:  time.Since(job.Created).Milliseconds(),
					PayloadLen: len(job.Payload),
				}
				chantrace.Send(parsed, p)
				handled.Add(1)

				if job.ID%9 == 0 {
					chantrace.Send(alerts, Alert{
						JobID:  job.ID,
						Level:  "warn",
						Reason: "divisible by 9",
					})
				}
			}
		})
	}

	chantrace.Go(ctx, "closer", func(context.Context) {
		workerWG.Wait()
		chantrace.Close(parsed)
		chantrace.Close(alerts)
	})

	chantrace.Go(ctx, "aggregator", func(context.Context) {
		parsedCh := (<-chan Parsed)(parsed)
		alertCh := (<-chan Alert)(alerts)

		for parsedCh != nil || alertCh != nil {
			chantrace.Select(
				chantrace.OnRecvOK(parsedCh, func(v Parsed, ok bool) {
					if !ok {
						parsedCh = nil
						return
					}
					parsedCount.Add(1)
				}),
				chantrace.OnRecvOK(alertCh, func(v Alert, ok bool) {
					if !ok {
						alertCh = nil
						return
					}
					alertCount.Add(1)
				}),
				chantrace.OnDefault(func() {
					time.Sleep(10 * time.Millisecond)
				}),
			)
		}

		chantrace.Close(done)
	})

	chantrace.Go(ctx, "stats", func(context.Context) {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Printf("created=%d handled=%d parsed=%d alerts=%d\n",
					created.Load(),
					handled.Load(),
					parsedCount.Load(),
					alertCount.Load(),
				)
			}
		}
	})

	<-ctx.Done()
	fmt.Println("shutdown signal received, draining pipeline...")

	select {
	case <-done:
		fmt.Println("pipeline drained")
	case <-time.After(8 * time.Second):
		fmt.Println("drain timeout; forcing shutdown")
	}

	fmt.Printf("final: created=%d handled=%d parsed=%d alerts=%d\n",
		created.Load(),
		handled.Load(),
		parsedCount.Load(),
		alertCount.Load(),
	)
}
