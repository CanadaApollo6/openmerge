package queue

import "testing"

func TestReviewEmptyCancellationReportRoundTrip(t *testing.T) {
	for _, unsupported := range []bool{false, true} {
		event := CheckCancellationReported{BatchID: "b1", TestedSHA: "sha", Unsupported: unsupported, Detail: "provider limitation"}
		_, wire, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeEvent(wire); err != nil {
			t.Errorf("unsupported=%t: %s: %v", unsupported, wire, err)
		}
	}
}
