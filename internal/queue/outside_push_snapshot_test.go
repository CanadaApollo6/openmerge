package queue

import (
	"bytes"
	"testing"
)

func TestOutsidePushEmptyHistoryReplay(t *testing.T) {
	for _, known := range []bool{false, true} {
		event := OutsidePushRecorded{PreviousSHA: "old", NewSHA: "new", HistoryKnown: known, VerificationBeforeLanding: "none", At: epoch}
		_, data, err := EncodeEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := EncodeSnapshot(Apply(State{}, event))
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := EncodeSnapshot(Apply(State{}, decoded))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(direct, []byte(`"commits":[]`)) {
			t.Fatal("projection did not normalize empty commits")
		}
		if !bytes.Equal(direct, replayed) {
			t.Errorf("history_known=%t: direct contains null=%t; replay contains empty array=%t", known, bytes.Contains(direct, []byte(`"commits":null`)), bytes.Contains(replayed, []byte(`"commits":[]`)))
		}
	}
}
