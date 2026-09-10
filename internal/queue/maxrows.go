package queue

import (
	"context"
	"log/slog"
	"time"
)

// maxEvictMultiplier bounds how far a single sweep can scale evictBatchSize
// up to close a large overshoot in fewer ticks, while still capping the
// size of any single DELETE. See RunMaxRowsSweeper's doc comment.
const maxEvictMultiplier = 10

// RunMaxRowsSweeper evicts oldest non-critical rows whenever data_queue's
// total row count (across every status) exceeds maxRows — a direct cap on
// the queue itself ("keep at most N rows, overwrite the oldest once
// full"), independent of the disk-wide storage_full_percent safety net in
// storagepolicy.go, which also has to account for non-queue disk usage on
// the same volume. Runs once immediately, then every interval, until ctx
// is cancelled.
//
// Each sweep evicts enough rows to close the overshoot (total - maxRows)
// in one pass, not just a flat evictBatchSize — found live on 2026-09-10
// that a flat per-tick batch can't outrun even a modest sustained insert
// rate once the queue is significantly over cap (evictBatchSize=1000/60s
// barely exceeded a ~15 rows/sec write rate, so the queue settled into a
// new equilibrium ~230,000 rows above the configured cap instead of ever
// draining back to it). The per-tick amount is still capped at
// evictBatchSize*maxEvictMultiplier so a very large overshoot drains over
// several ticks rather than one huge DELETE; the common case (already at
// or only slightly over cap) still evicts exactly evictBatchSize per tick,
// unchanged from before.
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

		toEvict := int64(evictBatchSize)
		if overshoot := stats.TotalCount - int64(maxRows); overshoot > toEvict {
			toEvict = overshoot
		}
		if ceiling := int64(evictBatchSize) * maxEvictMultiplier; toEvict > ceiling {
			toEvict = ceiling
		}

		n, err := repo.EvictOldestNonCritical(ctx, int(toEvict))
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
