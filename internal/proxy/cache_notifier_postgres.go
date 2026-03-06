package proxy

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const pgCacheInvalidateChannel = "cache_invalidate"

// PostgresCacheNotifier implements CacheNotifier using PostgreSQL LISTEN/NOTIFY.
type PostgresCacheNotifier struct {
	dsn string
	db  *sql.DB
}

// NewPostgresCacheNotifier creates a notifier. The db is used for NOTIFY
// (publish), and a separate pgx connection is opened for LISTEN (subscribe).
func NewPostgresCacheNotifier(dsn string, db *sql.DB) *PostgresCacheNotifier {
	return &PostgresCacheNotifier{dsn: dsn, db: db}
}

func (n *PostgresCacheNotifier) Publish(ctx context.Context, backend string) error {
	_, err := n.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", pgCacheInvalidateChannel, backend)
	return err
}

func (n *PostgresCacheNotifier) Subscribe(ctx context.Context, onInvalidate func(backend string)) {
	for {
		if ctx.Err() != nil {
			return
		}
		n.listen(ctx, onInvalidate)
		// Connection lost; wait before reconnecting.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (n *PostgresCacheNotifier) listen(ctx context.Context, onInvalidate func(backend string)) {
	conn, err := pgx.Connect(ctx, n.dsn)
	if err != nil {
		slog.Error("cache notifier: failed to connect to postgres", "error", err)
		return
	}
	defer conn.Close(context.Background())

	_, err = conn.Exec(ctx, "LISTEN "+pgCacheInvalidateChannel)
	if err != nil {
		slog.Error("cache notifier: LISTEN failed", "error", err)
		return
	}

	slog.Info("cache notifier: listening for invalidations on postgres")

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("cache notifier: notification wait failed", "error", err)
			return
		}
		onInvalidate(notification.Payload)
	}
}
