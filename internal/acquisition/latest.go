package acquisition

import (
	"sync"
	"time"
)

// LatestValue is the most recently observed reading for one Data Point.
type LatestValue struct {
	Value   *float64
	Quality string
	Unit    string
	At      time.Time
}

// LatestStore caches the most recent LatestValue per Data Point ID, fed by
// the acquisition engine's OnReading callback, for the Web UI's Data Points
// table (§16). Unlike status.Store (per-device connectivity), this is
// per-tag and holds the value itself, not just quality/timing.
type LatestStore struct {
	mu sync.RWMutex
	m  map[int64]LatestValue
}

func NewLatestStore() *LatestStore {
	return &LatestStore{m: make(map[int64]LatestValue)}
}

func (s *LatestStore) Update(datapointID int64, v LatestValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[datapointID] = v
}

func (s *LatestStore) Get(datapointID int64) (LatestValue, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[datapointID]
	return v, ok
}
