package main

import (
	"context"
	"fmt"

	"github.com/khzaw/chantrace"
)

func main() {
	chantrace.Enable(chantrace.WithLogStream())
	defer chantrace.Shutdown()

	ctx := context.Background()

	// Stage 1: generate numbers
	nums := chantrace.Make[int]("numbers", 5)
	chantrace.Go(ctx, "generator", func(_ context.Context) {
		for i := range 10 {
			chantrace.Send(nums, i+1)
		}
		chantrace.Close(nums)
	})

	// Stage 2: square them
	squared := chantrace.Make[int]("squared", 5)
	chantrace.Go(ctx, "squarer", func(_ context.Context) {
		for n := range chantrace.Range(nums) {
			chantrace.Send(squared, n*n)
		}
		chantrace.Close(squared)
	})

	// Stage 3: filter even
	even := chantrace.Make[int]("even", 5)
	chantrace.Go(ctx, "filter", func(_ context.Context) {
		for n := range chantrace.Range(squared) {
			if n%2 == 0 {
				chantrace.Send(even, n)
			}
		}
		chantrace.Close(even)
	})

	// Consume
	for n := range chantrace.Range(even) {
		fmt.Println("  result:", n)
	}
}
