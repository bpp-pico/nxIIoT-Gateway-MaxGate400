package api

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"nxiiot-gateway/internal/queue"
	"nxiiot-gateway/internal/storage"
)

func openStoreForwardTestDB(t *testing.T) *queue.Repository {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(dbPath, "../../migrations", slog.Default())
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	return queue.NewRepository(db)
}

func insertStoreForwardReading(t *testing.T, ctx context.Context, repo *queue.Repository) {
	t.Helper()
	v := 1.0
	if _, err := repo.Insert(ctx, queue.Entry{
		GatewayID:      "GW001",
		DeviceID:       1,
		DatapointID:    1,
		Value:          &v,
		Quality:        "GOOD",
		EventTimestamp: time.Now(),
		Priority:       "NORMAL",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// This is the fix for the 2026-09-10 "connection lost" incident: Stats()
// scans the whole data_queue table with no WHERE clause, and at production
// row counts that took 6-7s per call — cheap DB access is not guaranteed
// here even with correct indexes, so the endpoint must not re-run it on
// every poll.
func TestCachedQueueStatsReusesResultWithinTTL(t *testing.T) {
	ctx := context.Background()
	repo := openStoreForwardTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	insertStoreForwardReading(t, ctx, repo)

	s := &Server{queueRepo: repo}

	stats1, err := s.cachedQueueStats(ctx)
	if err != nil {
		t.Fatalf("cachedQueueStats: %v", err)
	}
	if stats1.TotalCount != 1 {
		t.Fatalf("TotalCount = %d, want 1", stats1.TotalCount)
	}

	insertStoreForwardReading(t, ctx, repo)

	stats2, err := s.cachedQueueStats(ctx)
	if err != nil {
		t.Fatalf("cachedQueueStats: %v", err)
	}
	if stats2.TotalCount != 1 {
		t.Fatalf("TotalCount = %d, want cached 1 (second insert should not be visible within the TTL)", stats2.TotalCount)
	}
}

func TestCachedQueueStatsRefreshesAfterTTL(t *testing.T) {
	ctx := context.Background()
	repo := openStoreForwardTestDB(t)
	if err := repo.EnsureGateway(ctx, "GW001", "Test Gateway"); err != nil {
		t.Fatalf("EnsureGateway: %v", err)
	}
	insertStoreForwardReading(t, ctx, repo)

	s := &Server{queueRepo: repo}
	if _, err := s.cachedQueueStats(ctx); err != nil {
		t.Fatalf("cachedQueueStats: %v", err)
	}

	insertStoreForwardReading(t, ctx, repo)
	// Simulate TTL expiry without a real sleep.
	s.statsCacheAt = time.Now().Add(-2 * statsCacheTTL)

	stats, err := s.cachedQueueStats(ctx)
	if err != nil {
		t.Fatalf("cachedQueueStats: %v", err)
	}
	if stats.TotalCount != 2 {
		t.Fatalf("TotalCount = %d, want 2 after cache expiry", stats.TotalCount)
	}
}
