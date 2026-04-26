package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Event struct {
	EventID      string
	CapturedAt   time.Time
	EndpointType string
	PayloadJSON  string
	SyncedAt     *time.Time
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("store mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store open: %w", err)
	}
	db.SetMaxOpenConns(1)

	schema, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), "schema.sql"))
	if err != nil {
		// Embedded schema fallback
		schema = []byte(embeddedSchema)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return nil, fmt.Errorf("store schema: %w", err)
	}
	return &Store{db: db}, nil
}

func OpenWithSchema(path, schemaPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("store mkdir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store open: %w", err)
	}
	db.SetMaxOpenConns(1)

	schema, err := os.ReadFile(schemaPath)
	if err != nil {
		schema = []byte(embeddedSchema)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return nil, fmt.Errorf("store schema: %w", err)
	}
	return &Store{db: db}, nil
}

func OpenInMemory() (*Store, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(embeddedSchema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Insert(e Event) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO events (event_id, captured_at, endpoint_type, payload_json)
		 VALUES (?, ?, ?, ?)`,
		e.EventID, e.CapturedAt.UTC(), e.EndpointType, e.PayloadJSON,
	)
	return err
}

func (s *Store) Unsynced(limit int) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT event_id, captured_at, endpoint_type, payload_json
		 FROM events WHERE synced_at IS NULL ORDER BY captured_at ASC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.EventID, &e.CapturedAt, &e.EndpointType, &e.PayloadJSON); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) MarkSynced(eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(eventIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(
		fmt.Sprintf("UPDATE events SET synced_at = ? WHERE event_id IN (%s)", placeholders),
		append([]any{now}, args...)...,
	)
	return err
}

func (s *Store) UnsyncedCount() (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM events WHERE synced_at IS NULL").Scan(&count)
	return count, err
}

const embeddedSchema = `
CREATE TABLE IF NOT EXISTS events (
  event_id      TEXT PRIMARY KEY,
  captured_at   TIMESTAMP NOT NULL,
  endpoint_type TEXT NOT NULL,
  payload_json  TEXT NOT NULL,
  synced_at     TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_events_synced   ON events(synced_at);
CREATE INDEX IF NOT EXISTS idx_events_captured ON events(captured_at);
`
