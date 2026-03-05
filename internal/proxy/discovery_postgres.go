package proxy

import (
	"context"
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresAccountSource fetches account IDs by running a SQL query against a
// PostgreSQL database. The query must return a single text column.
type PostgresAccountSource struct {
	db    *sql.DB
	query string
}

// NewPostgresAccountSource opens a connection pool using the provided DSN and
// stores the query to execute on each FetchAccounts call.
func NewPostgresAccountSource(dsn, query string) (*PostgresAccountSource, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	return &PostgresAccountSource{db: db, query: query}, nil
}

// FetchAccounts executes the configured query and returns all values from the
// first column as account IDs.
func (s *PostgresAccountSource) FetchAccounts(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var accounts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		accounts = append(accounts, id)
	}
	return accounts, rows.Err()
}

// Close shuts down the underlying connection pool.
func (s *PostgresAccountSource) Close() error {
	return s.db.Close()
}
