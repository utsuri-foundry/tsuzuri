package tsuzuri_test

import (
	"context"
	"fmt"
	"time"

	tsuzuri "github.com/utsuri-foundry/tsuzuri"
)

func ExampleClient_Query() {
	client, err := tsuzuri.Open(".")
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Capability discovery works before a task store is configured.
	result, err := client.Query(ctx, tsuzuri.Query{Command: "compatibility"})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Command, result.Schema, result.OK)
	// Output: compatibility 1 true
}
