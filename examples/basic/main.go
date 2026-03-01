package main

import (
	"context"
	"fmt"
	"time"

	"github.com/khzaw/chantrace"
)

type Order struct {
	ID    int
	Item  string
	Price float64
}

func main() {
	chantrace.Enable(chantrace.WithLogStream())
	defer chantrace.Shutdown()

	ctx := context.Background()

	orders := chantrace.Make[Order]("orders", 5)
	done := chantrace.Make[struct{}]("done")

	chantrace.Go(ctx, "producer", func(_ context.Context) {
		for i := range 5 {
			chantrace.Send(orders, Order{
				ID:    i + 1,
				Item:  fmt.Sprintf("item-%d", i+1),
				Price: float64(i+1) * 9.99,
			})
		}
		chantrace.Close(orders)
	})

	chantrace.Go(ctx, "consumer", func(_ context.Context) {
		for order := range chantrace.Range(orders) {
			fmt.Printf("  processed: Order{ID:%d, Item:%q, Price:%.2f}\n",
				order.ID, order.Item, order.Price)
		}
		chantrace.Send(done, struct{}{})
	})

	chantrace.Recv(done)
	time.Sleep(10 * time.Millisecond) // let final events flush
}
