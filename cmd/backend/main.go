package main

import (
	"context"
	"fmt"
	"log"
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
			log.Println("Failed to register backend in Redis, retrying...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		log.Printf("Registered backend '%s' in Redis\n", backendName)
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

	log.Printf("Starting backend %s on port %s\n", backendName, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
