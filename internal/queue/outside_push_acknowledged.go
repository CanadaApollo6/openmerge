package queue

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// OutsidePushAcknowledged records operator awareness, never authorization,
// authorship, verification, or a change to queue state. The store validates the
// source in the same queue before appending this receipt.
type OutsidePushAcknowledged struct {
	SourceEventID int64     `json:"source_event_id"`
	SHA           string    `json:"sha"`
	Operator      string    `json:"operator"`
	At            time.Time `json:"at"`
}

func (OutsidePushAcknowledged) queueEvent() {}

// Validate requires an immutable source identity, a supplied name, and time.
func (e OutsidePushAcknowledged) Validate() error {
	if e.SourceEventID <= 0 || e.SHA == "" || len(e.SHA) > 256 || strings.IndexFunc(e.SHA, unicode.IsSpace) >= 0 || strings.TrimSpace(e.Operator) == "" || len(e.Operator) > 256 || strings.IndexFunc(e.Operator, unicode.IsControl) >= 0 || e.At.IsZero() {
		return fmt.Errorf("invalid outside push acknowledgement")
	}
	return nil
}
