package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	backendName := os.Getenv("BACKEND_NAME")
	port := os.Getenv("PORT")
	redisAddr := os.Getenv("REDIS_ADDR")

	ctx := context.Background()
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	backendAddr := fmt.Sprintf("http://%s:%s", backendName, port)

	// Register backend in Redis
	for {
		err := rdb.SAdd(ctx, "backends:active", backendAddr).Err()
		if err != nil {
			log.Println("Failed to register backend in Redis, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("Registered backend '%s' in Redis\n", backendName)
		break
	}

	mux := http.NewServeMux()

	// HTTP handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello from %s\n", backendName)
	})

	// WebSocket handler
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("WebSocket upgrade failed:", err)
			return
		}
		defer conn.Close()

		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				log.Println("WebSocket read error:", err)
				break
			}

			if string(message) == "ping" {
				conn.WriteMessage(mt, []byte("pong"))
			} else {
				response := fmt.Sprintf("%s echo: %s", backendName, string(message))
				if err := conn.WriteMessage(mt, []byte(response)); err != nil {
					log.Println("WebSocket write error:", err)
					break
				}
			}
		}
	})

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}

	// Start server in a goroutine so it doesn't block signal handling.
	go func() {
		log.Printf("Starting backend %s on port %s\n", backendName, port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("backend listen error: %v", err)
		}
	}()

	// Wait for SIGTERM or SIGINT.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	log.Printf("received signal %s, shutting down backend %s...", sig, backendName)

	// Deregister from Redis so the proxy stops sending new requests.
	if err := rdb.SRem(ctx, "backends:active", backendAddr).Err(); err != nil {
		log.Printf("failed to deregister backend from Redis: %v", err)
	} else {
		log.Printf("deregistered backend '%s' from Redis", backendName)
	}

	// Give in-flight requests up to 15 seconds to complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("backend forced shutdown: %v", err)
	}

	log.Printf("backend %s shutdown complete", backendName)
}
