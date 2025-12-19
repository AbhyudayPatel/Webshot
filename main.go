package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shotlink/core"
)

func main() {
	// Graceful shutdown handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal, cleaning up...")
		core.Shutdown()
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("webshot - High-Performance Screenshot Service\nEndpoints:\n  /get?url=<URL>&width=<W>&height=<H>\n  /health"))
	})

	http.HandleFunc("/get", core.HandleScreenshot)
	http.HandleFunc("/health", core.HandleHealth)
	
	log.Println("webshot service running at http://localhost:8080/")
	log.Println("Use /health for monitoring and /get?url=<URL> for screenshots")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
