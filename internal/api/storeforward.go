package api

import (
	"context"
	"net/http"
	"time"

	"nxiiot-gateway/internal/queue"
	"nxiiot-gateway/internal/storage"
)

// statsCacheTTL bounds how often getStoreForwardStatus re-runs
// queue.Repository.Stats(). That query has no WHERE clause (it needs a true
// total across all statuses in one pass) so no index can make it cheap —
// at production row counts (600k+ rows) a single call took 6-7s. The
// Store & Forward and Dashboard pages both poll this endpoint (every 3s and
// 5s respectively) without waiting for the previous request to finish, so
// once the query got slower than the poll interval, requests piled up
// without bound, pinned the CPU, and starved the MQTT client's own
// keepalive handling — the 2026-09-10 "connection lost" incident. Caching
// here (rather than inside Stats() itself) keeps the maxrows eviction
// sweeper's own Stats() calls always fresh, since it depends on an
// up-to-date count to decide how much to evict.
const statsCacheTTL = 5 * time.Second

// cachedQueueStats returns queue.Repository.Stats(), reusing the last
// result if it's within statsCacheTTL. Holding the lock across a cache-miss
// query is deliberate: it coalesces concurrent callers into a single
// underlying query instead of letting them all run the expensive scan in
// parallel.
func (s *Server) cachedQueueStats(ctx context.Context) (queue.Stats, error) {
	s.statsCacheMu.Lock()
	defer s.statsCacheMu.Unlock()

	if time.Since(s.statsCacheAt) < statsCacheTTL {
		return s.statsCache, nil
	}

	stats, err := s.queueRepo.Stats(ctx)
	if err != nil {
		return queue.Stats{}, err
	}
	s.statsCache = stats
	s.statsCacheAt = time.Now()
	return stats, nil
}

// writeRateWindow is the lookback window queueWriteRatePerSec averages
// over — short enough to reflect the current polling cadence, long enough
// to not be noisy tick-to-tick.
const writeRateWindow = 30 * time.Second

// queueWriteRatePerSec estimates the live data_queue insert rate
// (rows/sec) so operators can compare it against eviction capacity
// (queue.evict_batch_size / queue.max_rows_sweep_interval_seconds) —
// found necessary live on 2026-09-09 when a low evict_batch_size couldn't
// keep pace with real acquisition throughput and the queue kept growing
// past max_rows regardless of eviction running correctly. Shared by the
// Store & Forward status and Diagnostics endpoints.
func (s *Server) queueWriteRatePerSec(ctx context.Context) (float64, error) {
	n, err := s.queueRepo.CountInsertedSince(ctx, time.Now().Add(-writeRateWindow))
	if err != nil {
		return 0, err
	}
	return float64(n) / writeRateWindow.Seconds(), nil
}

// storeForwardStatusDTO matches §16's Store & Forward panel fields.
type storeForwardStatusDTO struct {
	PendingRecords     int64      `json:"pending_records"`
	SendingRecords     int64      `json:"sending_records"`
	OldestPending      *time.Time `json:"oldest_pending,omitempty"`
	NewestPending      *time.Time `json:"newest_pending,omitempty"`
	RetryCount         int64      `json:"retry_count"`
	StorageUsedPercent *float64   `json:"storage_used_percent,omitempty"`
	StorageLevel       string     `json:"storage_level,omitempty"`
	ServerConnected    bool       `json:"server_connected"`
	ServerLastError    string     `json:"server_last_error,omitempty"`
	ServerLastSentAt   *time.Time `json:"server_last_sent_at,omitempty"`
	TotalRows          int64      `json:"total_rows"`
	MaxRows            int        `json:"max_rows"`
	WriteRatePerSec    float64    `json:"write_rate_per_sec"`
}

func (s *Server) getStoreForwardStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := s.cachedQueueStats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dto := storeForwardStatusDTO{
		PendingRecords: stats.PendingCount,
		SendingRecords: stats.SendingCount,
		OldestPending:  stats.OldestPending,
		NewestPending:  stats.NewestPending,
		RetryCount:     stats.TotalRetries,
		TotalRows:      stats.TotalCount,
		MaxRows:        s.cfg.Queue.MaxRows,
	}

	if rate, err := s.queueWriteRatePerSec(r.Context()); err == nil {
		dto.WriteRatePerSec = rate
	} else {
		s.log.Warn("failed to compute queue write rate", "error", err)
	}

	if pct, err := storage.DiskUsagePercent(s.cfg.Database.Path); err == nil {
		dto.StorageUsedPercent = &pct
		dto.StorageLevel = string(queue.ClassifyStorageLevel(pct, s.cfg.Queue.StorageFullPercent))
	} else {
		s.log.Warn("failed to read disk usage for store-forward status", "error", err)
	}

	if s.forwarder != nil {
		st := s.forwarder.Status()
		dto.ServerConnected = st.Connected
		dto.ServerLastError = st.LastError
		dto.ServerLastSentAt = st.LastSuccessAt
	}

	writeJSON(w, http.StatusOK, dto)
}

// getStoreForwardStatistics is currently the same view as status; kept as
// a separate endpoint per §21's API list since a dashboard vs. a
// diagnostics page may reasonably want different shapes later (e.g.
// historical throughput), which status alone doesn't cover.
func (s *Server) getStoreForwardStatistics(w http.ResponseWriter, r *http.Request) {
	s.getStoreForwardStatus(w, r)
}
