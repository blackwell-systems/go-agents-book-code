// Command tracker serves the book's issue tracker.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

func main() {
	addr := flag.String("addr", "localhost:8090", "listen address")
	data := flag.String("data", "issues.json", "file the issues are kept in")
	flag.Parse()
	s, err := tracker.Open(*data, time.Now)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{Addr: *addr, Handler: tracker.NewHandler(s), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("tracker listening on %s, issues in %s", *addr, *data)
	log.Fatal(hs.ListenAndServe())
}
