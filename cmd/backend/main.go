package main

import (
	"context"
	"fmt"
	"log/slog"
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
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
			slog.Error("failed to register backend in redis, retrying", "backend", backendName, "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("registered backend in redis", "backend", backendName)
		break
	}

	mux := http.NewServeMux()

	// Health check endpoint (liveness probe)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// HTTP handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "Hello from %s\n", backendName)
	})

	// WebSocket handler
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "backend", backendName, "error", err)
			return
		}
		defer func() { _ = conn.Close() }()

		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				slog.Error("websocket read error", "backend", backendName, "error", err)
				break
			}

			if string(message) == "ping" {
				_ = conn.WriteMessage(mt, []byte("pong"))
			} else {
				response := fmt.Sprintf("%s echo: %s", backendName, string(message))
				if err := conn.WriteMessage(mt, []byte(response)); err != nil {
					slog.Error("websocket write error", "backend", backendName, "error", err)
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
		slog.Info("starting backend", "backend", backendName, "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("backend listen error", "backend", backendName, "error", err)
			os.Exit(1)
		}
	}()

	// Wait for SIGTERM or SIGINT.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGTERM, syscall.SIGINT)
	sig := <-quit
	slog.Info("received signal, shutting down backend", "signal", sig.String(), "backend", backendName)

	// Deregister from Redis so the proxy stops sending new requests.
	if err := rdb.SRem(ctx, "backends:active", backendAddr).Err(); err != nil {
		slog.Error("failed to deregister backend from redis", "backend", backendName, "error", err)
	} else {
		slog.Info("deregistered backend from redis", "backend", backendName)
	}

	// Give in-flight requests up to 15 seconds to complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("backend forced shutdown", "backend", backendName, "error", err)
		cancel()
		os.Exit(1)
	}

	slog.Info("backend shutdown complete", "backend", backendName)
}
