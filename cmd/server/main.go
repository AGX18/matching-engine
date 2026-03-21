// Entry point. Wires all components and starts the server.
//
// Startup sequence (order matters):
//   1. Create Engine (allocates book + channels)
//   2. Create Broadcaster (wires to engine's event channel)
//   3. Create REST handler (wires to engine's command channel)
//   4. Start Engine goroutine (begins processing)
//   5. Start Broadcaster goroutine
//   6. Start HTTP server (begins accepting connections)

package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agx18/matching-engine/internal/api"
	"github.com/agx18/matching-engine/internal/engine"
)

func main() {
	symbol := envOrDefault("SYMBOL", "BTC/USD")
	addr := envOrDefault("LISTEN_ADDR", ":8080")

	log.Printf("=== Matching Engine starting for %s on %s ===", symbol, addr)

	// restHandler → eng.CommandCh() → engine → eng.Events() → broadcaster

	//  Engine: 256-deep command buffer (concurrent API requests queue here),
	//  512-deep event buffer (absorbs bursts between broadcast cycles).
	eng := engine.New(symbol, 256, 512)

	// Broadcaster reads from the engine's read-only event channel.
	bcast := api.NewBroadcaster(eng.Events())

	// REST handler sends to the engine's write-only command channel.
	restHandler := api.NewHandler(eng, eng.CommandCh())

	// Start background goroutines.
	go eng.Run()
	go bcast.Run()

	// Set up HTTP routing.
	mux := http.NewServeMux()
	restHandler.RegisterRoutes(mux)
	mux.HandleFunc("GET /ws", bcast.ServeWS) // WebSocket live feed

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGTERM / SIGINT.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		log.Printf("HTTP server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutdown signal received...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shutdown sequence — order is critical:
	// 1. Stop HTTP server first — drains all in-flight handlers.
	//    No new commands can reach the engine after this returns.
	// 2. Close the engine — safe now because no handler can send to commandCh.
	//    Run() drains any buffered commands then closes eventCh.
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}

	eng.Close()
	log.Println("Engine stopped. Goodbye.")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
