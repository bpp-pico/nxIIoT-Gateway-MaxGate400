package queue

import (
	"context"
	"log/slog"
	"time"
)

// RunMaxRowsSweeper evicts oldest non-critical rows whenever data_queue's
// total row count (across every status) exceeds maxRows — a direct cap on
// the queue itself ("keep at most N rows, overwrite the oldest once
// full"), independent of the disk-wide storage_full_percent safety net in
// storagepolicy.go, which also has to account for non-queue disk usage on
// the same volume. Runs once immediately, then every interval, until ctx
// is cancelled.
func RunMaxRowsSweeper(ctx context.Context, repo *Repository, maxRows, evictBatchSize int, interval time.Duration, log *slog.Logger) {
	sweep := func() {
		stats, err := repo.Stats(ctx)
		if err != nil {
			log.Error("max-rows sweep: failed to count queue rows", "error", err)
			return
		}
		if stats.TotalCount <= int64(maxRows) {
			return
		}

		n, err := repo.EvictOldestNonCritical(ctx, evictBatchSize)
		if err != nil {
			log.Error("max-rows sweep: eviction failed", "error", err)
			return
		}
		if n > 0 {
			log.Warn("queue over max_rows: evicted oldest non-critical rows",
				"total_rows", stats.TotalCount, "max_rows", maxRows, "evicted", n)
		} else {
			log.Error("queue over max_rows and nothing left to evict (only CRITICAL data remains)",
				"total_rows", stats.TotalCount, "max_rows", maxRows)
		}
	}

	sweep()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep()
		}
	}
}
