-- EvictOldestNonCritical (internal/queue/storagepolicy.go) now deletes
-- per priority tier ("WHERE priority = ? ORDER BY event_timestamp ASC
-- LIMIT ?") instead of one query sorting by a computed CASE-priority
-- expression, which had no supporting index at all. This index makes
-- each tier's delete a cheap index range scan instead of a full table
-- sort. Found necessary in the same 2026-09-10 incident as migration
-- 0006 -- see MEMORY.md.
CREATE INDEX IF NOT EXISTS idx_data_queue_priority_event_timestamp ON data_queue(priority, event_timestamp);
