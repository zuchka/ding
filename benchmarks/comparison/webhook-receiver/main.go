package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := "9999"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Print nanosecond timestamp immediately; run.sh tails this output.
		fmt.Printf("%d\n", time.Now().UnixNano())
		w.WriteHeader(http.StatusOK)
	})
	log.Printf("webhook-receiver listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
