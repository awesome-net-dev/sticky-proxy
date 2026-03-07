package proxy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore creates a PostgresStore and auto-creates required tables.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	s := &PostgresStore{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS backends (
			url TEXT PRIMARY KEY,
			healthy BOOLEAN NOT NULL DEFAULT true,
			discovered_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		CREATE TABLE IF NOT EXISTS assignments (
			routing_key TEXT PRIMARY KEY,
			backend TEXT NOT NULL,
			assigned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			source TEXT NOT NULL DEFAULT 'assignment',
			weight INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX IF NOT EXISTS idx_assignments_backend ON assignments(backend);
		ALTER TABLE assignments ADD COLUMN IF NOT EXISTS weight INTEGER NOT NULL DEFAULT 1;
	`)
	return err
}

func (s *PostgresStore) AssignLeastLoaded(ctx context.Context, routingKey string) (*Assignment, error) {
	var a Assignment
	err := s.db.QueryRowContext(ctx, `
		WITH target AS (
			SELECT b.url FROM backends b
			LEFT JOIN assignments a ON a.backend = b.url
			WHERE b.healthy = true
			GROUP BY b.url
			ORDER BY COALESCE(SUM(a.weight), 0) ASC
			LIMIT 1
		)
		INSERT INTO assignments (routing_key, backend, source, weight)
		SELECT $1, url, 'assignment', 1 FROM target
		ON CONFLICT (routing_key) DO NOTHING
		RETURNING backend, assigned_at, source, weight
	`, routingKey).Scan(&a.Backend, &a.AssignedAt, &a.Source, &a.Weight)
	if err == sql.ErrNoRows {
		// Conflict — assignment already exists.
		return s.GetAssignment(ctx, routingKey)
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *PostgresStore) GetAssignment(ctx context.Context, routingKey string) (*Assignment, error) {
	var a Assignment
	err := s.db.QueryRowContext(ctx,
		`SELECT backend, assigned_at, source, weight FROM assignments WHERE routing_key = $1`,
		routingKey,
	).Scan(&a.Backend, &a.AssignedAt, &a.Source, &a.Weight)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *PostgresStore) GetAllAssignments(ctx context.Context) (result map[string]*Assignment, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT routing_key, backend, assigned_at, source, weight FROM assignments`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()

	result = make(map[string]*Assignment)
	for rows.Next() {
		var key string
		var a Assignment
		if err := rows.Scan(&key, &a.Backend, &a.AssignedAt, &a.Source, &a.Weight); err != nil {
			return nil, err
		}
		result[key] = &a
	}
	return result, rows.Err()
}

func (s *PostgresStore) GetBackendUsers(ctx context.Context, backend string) (users []string, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT routing_key FROM assignments WHERE backend = $1`, backend)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()

	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		users = append(users, key)
	}
	return users, rows.Err()
}

func (s *PostgresStore) BulkAssign(ctx context.Context, assignments map[string]BulkAssignEntry) (map[string]string, error) {
	if len(assignments) == 0 {
		return nil, nil
	}

	now := time.Now().UTC()

	// Build multi-row INSERT with parameterized values.
	var query strings.Builder
	query.WriteString("INSERT INTO assignments (routing_key, backend, assigned_at, source, weight) VALUES ")
	args := []any{now} // $1 = timestamp
	idx := 2
	first := true
	for routingKey, entry := range assignments {
		if !first {
			query.WriteString(", ")
		}
		w := entry.Weight
		if w <= 0 {
			w = 1
		}
		fmt.Fprintf(&query, "($%d, $%d, $1, 'assignment', $%d)", idx, idx+1, idx+2)
		args = append(args, routingKey, entry.Backend, w)
		idx += 3
		first = false
	}
	query.WriteString(" ON CONFLICT (routing_key) DO NOTHING RETURNING routing_key, backend")

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		// rows.Close error is non-critical here since we already read all rows.
		_ = rows.Close()
	}()

	assigned := make(map[string]string)
	for rows.Next() {
		var key, backend string
		if err := rows.Scan(&key, &backend); err != nil {
			return nil, err
		}
		assigned[key] = backend
	}
	return assigned, rows.Err()
}

func (s *PostgresStore) BulkDeleteAssignments(ctx context.Context, routingKeys []string) error {
	if len(routingKeys) == 0 {
		return nil
	}

	placeholders := make([]string, len(routingKeys))
	args := make([]any, len(routingKeys))
	for i, key := range routingKeys {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = key
	}
	query := "DELETE FROM assignments WHERE routing_key IN (" + strings.Join(placeholders, ", ") + ")"
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *PostgresStore) ActiveBackends(ctx context.Context) (backends []string, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT url FROM backends WHERE healthy = true`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()

	for rows.Next() {
		var url string
		if err := rows.Scan(&url); err != nil {
			return nil, err
		}
		backends = append(backends, url)
	}
	return backends, rows.Err()
}

func (s *PostgresStore) AddBackend(ctx context.Context, backend string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backends (url, healthy) VALUES ($1, true)
		 ON CONFLICT (url) DO UPDATE SET healthy = true`,
		backend)
	return err
}

func (s *PostgresStore) RemoveBackend(ctx context.Context, backend string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE backends SET healthy = false WHERE url = $1`, backend)
	return err
}

func (s *PostgresStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// DB returns the underlying database pool, used by PostgresCacheNotifier.
func (s *PostgresStore) DB() *sql.DB {
	return s.db
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

// --- Distributed locking ---

// pgTransitionLockID is the advisory lock key used for drain/rebalance mutual
// exclusion across replicas. The value is arbitrary but must be consistent.
const pgTransitionLockID = 0x5350_5458 // "SPTX" in hex

// PostgresDistributedLocker implements DistributedLocker using PostgreSQL
// session-level advisory locks. The lock is held for the duration of the
// database connection and is automatically released if the connection drops.
type PostgresDistributedLocker struct {
	db   *sql.DB
	conn *sql.Conn // non-nil while lock is held
}

// NewPostgresDistributedLocker creates a distributed locker backed by PostgreSQL.
func NewPostgresDistributedLocker(db *sql.DB) *PostgresDistributedLocker {
	return &PostgresDistributedLocker{db: db}
}

func (l *PostgresDistributedLocker) TryLock(ctx context.Context) (bool, error) {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return false, err
	}

	var acquired bool
	err = conn.QueryRowContext(ctx,
		"SELECT pg_try_advisory_lock($1)", pgTransitionLockID,
	).Scan(&acquired)
	if err != nil {
		_ = conn.Close()
		return false, err
	}
	if !acquired {
		_ = conn.Close()
		return false, nil
	}

	l.conn = conn
	return true, nil
}

func (l *PostgresDistributedLocker) Unlock(ctx context.Context) error {
	if l.conn == nil {
		return nil
	}
	_, err := l.conn.ExecContext(ctx,
		"SELECT pg_advisory_unlock($1)", pgTransitionLockID,
	)
	closeErr := l.conn.Close()
	l.conn = nil
	if err != nil {
		return err
	}
	return closeErr
}
