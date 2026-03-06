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

// FetchAccounts executes the configured query and returns discovered accounts.
// If the query returns two columns, the second is used as the account weight.
func (s *PostgresAccountSource) FetchAccounts(ctx context.Context) (accounts []DiscoveredAccount, err error) {
	rows, err := s.db.QueryContext(ctx, s.query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	hasWeight := len(cols) >= 2

	for rows.Next() {
		var acct DiscoveredAccount
		if hasWeight {
			if err := rows.Scan(&acct.ID, &acct.Weight); err != nil {
				return nil, err
			}
		} else {
			if err := rows.Scan(&acct.ID); err != nil {
				return nil, err
			}
		}
		accounts = append(accounts, acct)
	}
	return accounts, rows.Err()
}

// Close shuts down the underlying connection pool.
func (s *PostgresAccountSource) Close() error {
	return s.db.Close()
}
