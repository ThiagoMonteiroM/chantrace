package main

import (
	"fmt"

	"github.com/khzaw/chantrace"
)

func main() {
	chantrace.Enable(chantrace.WithLogStream())
	defer chantrace.Shutdown()

	ch := chantrace.Make[int]("select-ok", 1)
	chantrace.Send(ch, 42)
	close(ch)

	closed := false
	for !closed {
		chantrace.Select(
			chantrace.OnRecvOK(ch, func(v int, ok bool) {
				if !ok {
					fmt.Println("channel closed")
					closed = true
					return
				}
				fmt.Println("received:", v)
			}),
		)
	}

	chantrace.Unregister(ch)
}
