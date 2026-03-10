package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sticky-proxy/pkg/ownership"

	"github.com/gorilla/websocket"
	_ "github.com/jackc/pgx/v5/stdlib"
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
	pgDSN := os.Getenv("POSTGRES_DSN")

	ctx := context.Background()
	backendAddr := fmt.Sprintf("http://%s:%s", backendName, port)

	// Optional Redis connection for self-registration and ownership checking.
	var rdb *redis.Client
	var oc *ownership.Checker
	if redisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: redisAddr})

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

		oc = ownership.New(rdb, backendAddr, func(userID string) {
			slog.Warn("lost ownership, stopping background work", "userId", userID, "backend", backendName)
		})
		go oc.Start()
	} else {
		slog.Info("REDIS_ADDR not set, skipping redis registration and ownership checker")
	}

	// Optional PostgreSQL connection for /login account registration.
	var pgDB *sql.DB
	if pgDSN != "" {
		var err error
		pgDB, err = sql.Open("pgx", pgDSN)
		if err != nil {
			slog.Error("failed to connect to postgres", "error", err)
		} else {
			pgDB.SetMaxOpenConns(10)
			pgDB.SetMaxIdleConns(2)
			slog.Info("connected to postgres for account registration")
		}
	}

	mux := http.NewServeMux()

	// Health check endpoint (liveness probe)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Assign/Unassign hook handlers
	mux.HandleFunc("/hooks/assign", func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Users []string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if oc != nil {
			for _, user := range payload.Users {
				oc.Track(user)
			}
		}
		slog.Info("assign hook received", "users", len(payload.Users), "backend", backendName)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/hooks/unassign", func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Users []string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if oc != nil {
			for _, user := range payload.Users {
				oc.Untrack(user)
			}
		}
		slog.Info("unassign hook received", "users", len(payload.Users), "backend", backendName)
		w.WriteHeader(http.StatusOK)
	})

	// Login handler — registers new accounts in PostgreSQL and returns a
	// routing key via X-Sticky-Routing-Key header for sticky binding.
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			UserID string `json:"user_id"`
			Weight int    `json:"weight"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if payload.UserID == "" {
			http.Error(w, "user_id required", http.StatusBadRequest)
			return
		}
		if payload.Weight <= 0 {
			payload.Weight = 1
		}

		if pgDB != nil {
			_, err := pgDB.ExecContext(r.Context(),
				`INSERT INTO accounts (id, weight) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
				payload.UserID, payload.Weight)
			if err != nil {
				slog.Error("failed to register account", "userId", payload.UserID, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("X-Sticky-Routing-Key", payload.UserID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"user_id": payload.UserID,
			"backend": backendName,
		})
		slog.Info("login registered", "userId", payload.UserID, "backend", backendName)
	})

	// HTTP handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if oc != nil {
			if userID := r.Header.Get("X-User-ID"); userID != "" {
				oc.Track(userID)
			}
		}
		_, _ = fmt.Fprintf(w, "Hello from %s\n", backendName)
	})

	// WebSocket handler
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if oc != nil {
			if userID := r.Header.Get("X-User-ID"); userID != "" {
				oc.Track(userID)
			}
		}

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

	if oc != nil {
		oc.Stop()
	}
	if pgDB != nil {
		_ = pgDB.Close()
	}

	// Deregister from Redis so the proxy stops sending new requests.
	if rdb != nil {
		if err := rdb.SRem(ctx, "backends:active", backendAddr).Err(); err != nil {
			slog.Error("failed to deregister backend from redis", "backend", backendName, "error", err)
		} else {
			slog.Info("deregistered backend from redis", "backend", backendName)
		}
	}

	// Give in-flight requests up to 15 seconds to complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("backend forced shutdown", "backend", backendName, "error", err)
		cancel()
		os.Exit(1)
	}
	cancel()

	slog.Info("backend shutdown complete", "backend", backendName)
}
