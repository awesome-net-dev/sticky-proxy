package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

func main() {
	backendName := os.Getenv("BACKEND_NAME")
	if backendName == "" {
		backendName = "backend-default"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "5678"
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "redis:6379"
	}

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

	log.Printf("Starting backend %s on port %s\n", backendName, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
