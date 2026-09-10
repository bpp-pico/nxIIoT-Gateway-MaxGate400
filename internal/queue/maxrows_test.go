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

// TestRunMaxRowsSweeperScalesEvictionToOvershoot is the regression test for
// the 2026-09-10 incident: a flat evictBatchSize per tick couldn't outrun a
// modest sustained insert rate once the queue was significantly over cap,
// settling into a new equilibrium hundreds of thousands of rows above
// max_rows instead of ever draining back to it. A single sweep must now
// evict enough to close the overshoot (bounded by maxEvictMultiplier), not
// just evictBatchSize.
func TestRunMaxRowsSweeperScalesEvictionToOvershoot(t *testing.T) {
	ctx := context.Background()
	_, repo := openTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	for i := 0; i < 25; i++ {
		insertReading(t, ctx, repo, "NORMAL")
	}

	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	// max_rows=5, evict_batch_size=2: overshoot is 20, within the 10x
	// ceiling (20) — a single sweep should evict all 20 overshoot rows,
	// not just 2, leaving exactly max_rows behind.
	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	queue.RunMaxRowsSweeper(runCtx, repo, 5, 2, time.Hour, log)
	cancel()

	stats, err := repo.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalCount != 5 {
		t.Fatalf("TotalCount = %d, want 5 (one sweep should have closed the full overshoot)", stats.TotalCount)
	}
}

// TestRunMaxRowsSweeperCapsEvictionPerTick confirms a very large overshoot
// is still bounded to evictBatchSize*maxEvictMultiplier in a single sweep,
// rather than one unbounded DELETE.
func TestRunMaxRowsSweeperCapsEvictionPerTick(t *testing.T) {
	ctx := context.Background()
	_, repo := openTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	for i := 0; i < 100; i++ {
		insertReading(t, ctx, repo, "NORMAL")
	}

	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))

	// max_rows=5, evict_batch_size=2: overshoot is 95, far past the 10x
	// ceiling (20) — a single sweep must evict exactly 20, not 95.
	runCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	queue.RunMaxRowsSweeper(runCtx, repo, 5, 2, time.Hour, log)
	cancel()

	stats, err := repo.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.TotalCount != 80 {
		t.Fatalf("TotalCount = %d, want 80 (100 - 20 capped eviction)", stats.TotalCount)
	}
}
