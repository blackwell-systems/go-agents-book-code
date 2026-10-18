// Command eino runs chapter 16's task on Eino, against the
// scripted diagnose replies, and prints what it did. compare builds it
// to measure the size of each version's binary.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/blackwell-systems/go-agents-book/code/ch12/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch16/eino"
	"github.com/blackwell-systems/go-agents-book/code/ch16/task"
)

func main() {
	c, _ := task.Replay()
	env := &task.Env{}
	ctx := replay.WithScript(context.Background(), "diagnose")
	if _, err := eino.Run(ctx, c, env); err != nil {
		log.Fatal(err)
	}
	r := env.Report()
	fmt.Printf("issues filed %d, diagnosis submitted %v\n", r.Issues,
		r.Diagnosis != nil)
}
