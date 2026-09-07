package api

import (
	"testing"
	"time"

	"nxiiot-gateway/internal/acquisition"
)

func TestWithLatestFillsFieldsFromLatestStore(t *testing.T) {
	latest := acquisition.NewLatestStore()
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	value := 231.5
	latest.Update(7, acquisition.LatestValue{Value: &value, Quality: "GOOD", Unit: "V", At: at})

	s := &Server{latest: latest}
	dto := s.withLatest(dataPointDTO{ID: 7})

	if dto.LastValue == nil || *dto.LastValue != value {
		t.Fatalf("LastValue = %v, want %v", dto.LastValue, value)
	}
	if dto.LastQuality != "GOOD" {
		t.Fatalf("LastQuality = %q, want GOOD", dto.LastQuality)
	}
	if dto.LastReadAt == nil || !dto.LastReadAt.Equal(at) {
		t.Fatalf("LastReadAt = %v, want %v", dto.LastReadAt, at)
	}
}

func TestWithLatestLeavesFieldsUnsetWhenNeverPolled(t *testing.T) {
	latest := acquisition.NewLatestStore()
	s := &Server{latest: latest}

	dto := s.withLatest(dataPointDTO{ID: 42})

	if dto.LastValue != nil || dto.LastQuality != "" || dto.LastReadAt != nil {
		t.Fatalf("expected unset latest fields, got %+v", dto)
	}
}

func TestWithLatestNilStoreIsNoop(t *testing.T) {
	s := &Server{}
	dto := s.withLatest(dataPointDTO{ID: 1})
	if dto.LastValue != nil || dto.LastQuality != "" || dto.LastReadAt != nil {
		t.Fatalf("expected unset latest fields with nil store, got %+v", dto)
	}
}
