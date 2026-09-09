// Package queue persists acquired readings to the data_queue table — the
// gateway's local source of truth (Design Principle #2/#3). Data must be
// persisted here before being forwarded (Rule 2); the Store & Forward
// worker (Phase 4) will read PENDING rows from this table and mark them
// SENT/FAILED, but insertion is independent of any of that.
package queue

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const timeLayout = "2006-01-02T15:04:05.000Z"

type Entry struct {
	ID             int64
	GatewayID      string
	SequenceID     int64
	DeviceID       int64
	DatapointID    int64
	Value          *float64
	Quality        string
	EventTimestamp time.Time
	Priority       string
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// EnsureGateway upserts the gateway's own row, which holds the persistent
// sequence counter (Rule 6: "sequence must persist across Gateway
// restarts"). Call once at startup.
func (r *Repository) EnsureGateway(ctx context.Context, id, name string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO gateway (id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name`,
		id, name)
	return err
}

// Insert assigns the next sequence_id for gatewayID and persists the
// reading as a PENDING data_queue row, atomically, in a single transaction.
// gateway_id + sequence_id is the idempotency key the server uses to detect
// duplicates under at-least-once delivery (Rule 6/7).
func (r *Repository) Insert(ctx context.Context, e Entry) (Entry, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback()

	var seq int64
	err = tx.QueryRowContext(ctx,
		`UPDATE gateway SET last_sequence = last_sequence + 1 WHERE id = ? RETURNING last_sequence`,
		e.GatewayID,
	).Scan(&seq)
	if err != nil {
		return Entry{}, fmt.Errorf("assign sequence: %w", err)
	}
	e.SequenceID = seq

	res, err := tx.ExecContext(ctx, `
		INSERT INTO data_queue (gateway_id, sequence_id, device_id, datapoint_id, value, quality, event_timestamp, status, priority)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		e.GatewayID, e.SequenceID, e.DeviceID, e.DatapointID, e.Value, e.Quality,
		e.EventTimestamp.UTC().Format(timeLayout), priorityOrDefault(e.Priority))
	if err != nil {
		return Entry{}, fmt.Errorf("insert data_queue row: %w", err)
	}
	e.ID, err = res.LastInsertId()
	if err != nil {
		return Entry{}, err
	}

	if err := tx.Commit(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// CountInsertedSince returns how many rows have event_timestamp at or
// after since — data_queue keeps no separate "inserted_at" column, but
// EventTimestamp is assigned by the acquisition callback right before
// Insert writes the row, so it's an accurate proxy for a live write-rate
// estimate (rows-per-second) on the Dashboard/Diagnostics pages.
func (r *Repository) CountInsertedSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM data_queue WHERE event_timestamp >= ?`,
		since.UTC().Format(timeLayout)).Scan(&n)
	return n, err
}

func priorityOrDefault(p string) string {
	if p == "" {
		return "NORMAL"
	}
	return p
}
