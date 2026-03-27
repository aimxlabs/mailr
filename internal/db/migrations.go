package db

import (
	"database/sql"
	"fmt"
)

func Migrate(db *sql.DB, namespace string, migrations []string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS _migrations (
		namespace TEXT NOT NULL,
		version   INTEGER NOT NULL,
		applied_at TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')),
		PRIMARY KEY (namespace, version)
	)`); err != nil {
		return fmt.Errorf("creating migrations table: %w", err)
	}

	var current int
	row := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM _migrations WHERE namespace = ?", namespace)
	if err := row.Scan(&current); err != nil {
		return fmt.Errorf("getting migration version for %s: %w", namespace, err)
	}

	for i := current; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("beginning migration %s/%d: %w", namespace, i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("applying migration %s/%d: %w", namespace, i+1, err)
		}
		if _, err := tx.Exec("INSERT INTO _migrations (namespace, version) VALUES (?, ?)", namespace, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("recording migration %s/%d: %w", namespace, i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s/%d: %w", namespace, i+1, err)
		}
	}
	return nil
}
