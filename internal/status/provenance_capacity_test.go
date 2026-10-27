package status

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	pj "github.com/CanadaApollo6/openmerge/internal/provenance/journal"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func TestQueueStatusPublishesProvenanceCapacity(t *testing.T) {
	gauge := pj.CapacityGauge{FenceCount: 400000, ProjectedBytes: 23855490, LimitBytes: 33554432, HighWaterBytes: 26843545}
	q, _, _ := Project(store.Journal{ProvenanceCapacity: &gauge}, 0, 0, diagnostic.NewSanitizer())
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"provenance_capacity":{"fence_count":400000,"projected_bytes":23855490,"limit_bytes":33554432,"high_water_bytes":26843545}`) {
		t.Fatalf("capacity absent from status: %s", raw)
	}
}
