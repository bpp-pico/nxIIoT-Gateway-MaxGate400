package queue_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"nxiiot-gateway/internal/queue"
)

func TestRunMaxRowsSweeperDoesNotEvictUnderCap(t *testing.T) {
	ctx := context.Background()
	_, repo := openTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	insertReading(t, ctx, repo, "LOW")
	insertReading(t, ctx, repo, "LOW")

	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	// RunMaxRowsSweeper runs its sweep once immediately, before the ticker
	// even starts — that alone is enough to observe the behavior here, so
	// the tick interval is set far longer than the test's budget (only the
	// immediate sweep should ever fire). This avoids racing a real DB query
	// against the bounding context's deadline, which was flaky on Windows
	// (a query straddling the exact deadline moment could leave the SQLite
	// file transiently locked past db.Close(), failing TempDir cleanup).
	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	queue.RunMaxRowsSweeper(runCtx, repo, 5, 10, time.Hour, log)
	cancel()

	stats, err := repo.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalCount != 2 {
		t.Fatalf("TotalCount = %d, want 2 (nothing should be evicted under max_rows)", stats.TotalCount)
	}
}

func TestRunMaxRowsSweeperEvictsOldestNonCriticalOverCap(t *testing.T) {
	ctx := context.Background()
	_, repo := openTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	critical := insertReading(t, ctx, repo, "CRITICAL")
	insertReading(t, ctx, repo, "LOW")
	insertReading(t, ctx, repo, "NORMAL")

	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	// Cap of 1, evicting up to 10 per pass: both non-critical rows must go,
	// leaving only the CRITICAL row, never evicted regardless of cap. Long
	// tick interval so only the immediate sweep fires — see the comment in
	// TestRunMaxRowsSweeperDoesNotEvictUnderCap above.
	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	queue.RunMaxRowsSweeper(runCtx, repo, 1, 10, time.Hour, log)
	cancel()

	remaining, err := repo.FetchBatch(ctx, 10)
	if err != nil {
		t.Fatalf("FetchBatch: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != critical.ID {
		t.Fatalf("expected only the CRITICAL row to survive, got %+v", remaining)
	}
}
