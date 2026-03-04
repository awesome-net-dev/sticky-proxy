package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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

	// Register backend in Redis
	for {
		err := rdb.SAdd(ctx, "backends:active", fmt.Sprintf("http://%s:%s", backendName, port)).Err()
		if err != nil {
			slog.Error("failed to register backend in redis, retrying", "backend", backendName, "error", err)
			time.Sleep(2 * time.Second)
			continue
		}
		slog.Info("registered backend in redis", "backend", backendName)
		break
	}

	// HTTP handler
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello from %s\n", backendName)
	})

	// WebSocket handler
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "backend", backendName, "error", err)
			return
		}
		defer conn.Close()

		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				slog.Error("websocket read error", "backend", backendName, "error", err)
				break
			}

			if string(message) == "ping" {
				conn.WriteMessage(mt, []byte("pong"))
			} else {
				response := fmt.Sprintf("%s echo: %s", backendName, string(message))
				if err := conn.WriteMessage(mt, []byte(response)); err != nil {
					slog.Error("websocket write error", "backend", backendName, "error", err)
					break
				}
			}
		}
	})

	slog.Info("starting backend", "backend", backendName, "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("http server failed", "backend", backendName, "port", port, "error", err)
		os.Exit(1)
	}
}
