package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/khzaw/chantrace"
)

func main() {
	chantrace.Enable(chantrace.WithLogStream())
	defer chantrace.Shutdown()

	ctx := context.Background()

	jobs := chantrace.Make[int]("jobs", 10)
	results := chantrace.Make[string]("results", 10)

	// Fan-out: 3 workers
	var wg sync.WaitGroup
	for w := range 3 {
		wg.Add(1)
		name := fmt.Sprintf("worker-%d", w)
		chantrace.Go(ctx, name, func(_ context.Context) {
			defer wg.Done()
			for job := range chantrace.Range(jobs) {
				result := fmt.Sprintf("w%d:job%d", w, job)
				chantrace.Send(results, result)
			}
		})
	}

	// Producer
	chantrace.Go(ctx, "producer", func(_ context.Context) {
		for i := range 9 {
			chantrace.Send(jobs, i+1)
		}
		chantrace.Close(jobs)
	})

	// Collector
	chantrace.Go(ctx, "collector", func(_ context.Context) {
		wg.Wait()
		chantrace.Close(results)
	})

	for result := range chantrace.Range(results) {
		fmt.Println("  result:", result)
	}
}
