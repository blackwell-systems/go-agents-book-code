// Command tracker-mcp serves the book's issue tracker over MCP on its
// standard input and output, for any MCP client: the triage agent, or
// an assistant app.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch14/trackermcp"
)

// tag::main[]

func main() {
	data := flag.String("data", "issues.json",
		"file the issues are kept in")
	flag.Parse()
	log.SetOutput(os.Stderr) // standard output carries only messages
	err := trackermcp.Serve(context.Background(), *data, time.Now,
		os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		log.Fatal(err)
	}
}

// end::main[]
