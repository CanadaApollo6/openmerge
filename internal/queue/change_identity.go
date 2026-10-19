package queue

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ChangeID identifies a durable PR-backed change within its canonical queue.
// The v1 spelling is chg1_ followed by 64 lowercase hexadecimal digits.
type ChangeID string

// ChangeRevision is an observation sequence, not a commit hash. Zero means
// that no head has been observed. Returning to an old head advances the sequence.
type ChangeRevision uint64

// PRChangeID hashes the stored canonical queue key, never a discovered case
// alias, YAML queue name, or database ID. Each UTF-8 field (including the domain
// and decimal PR) has an unsigned 64-bit big-endian byte-length prefix. The
// domain fixes both the algorithm version and the PR-backed source kind.
func PRChangeID(installation, repository, base, lane string, pr int) (ChangeID, error) {
	if installation == "" || repository == "" || base == "" || pr <= 0 {
		return "", fmt.Errorf("canonical queue key and positive PR number are required")
	}
	h := sha256.New()
	for _, field := range []string{"openmerge/change/pr/v1", installation, repository, base, lane, strconv.Itoa(pr)} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		_, _ = h.Write(size[:])
		_, _ = h.Write([]byte(field))
	}
	return ChangeID("chg1_" + hex.EncodeToString(h.Sum(nil))), nil
}

// Validate rejects noncanonical IDs, including unknown encoding versions.
func (id ChangeID) Validate() error {
	raw := string(id)
	if !strings.HasPrefix(raw, "chg1_") || len(raw) != 69 || strings.ToLower(raw) != raw {
		return fmt.Errorf("invalid change ID %q", id)
	}
	if _, err := hex.DecodeString(raw[5:]); err != nil {
		return fmt.Errorf("invalid change ID: %w", err)
	}
	return nil
}

// RevisionObservationBasis records the queue-base tip and read time. Legacy
// PRObserved events have no source time; their At remains zero, not inferred.
type RevisionObservationBasis struct {
	BaseSHA string    `json:"base_sha"`
	At      time.Time `json:"at"`
}

// ChangeRevisionObserved adds revision evidence without replacing PRObserved's
// eligibility/trust contract. Production emission begins only at Stage A 11b.
// Predecessor is zero only for the first revision.
type ChangeRevisionObserved struct {
	PR          int                      `json:"pr"`
	ChangeID    ChangeID                 `json:"change_id"`
	Revision    ChangeRevision           `json:"revision"`
	Predecessor ChangeRevision           `json:"predecessor"`
	HeadSHA     string                   `json:"head_sha"`
	Basis       RevisionObservationBasis `json:"basis"`
}

func (ChangeRevisionObserved) queueEvent() {}

// Validate checks the value contract; CanObserveChangeRevision checks the fold.
func (e ChangeRevisionObserved) Validate() error {
	if err := e.ChangeID.Validate(); err != nil {
		return err
	}
	if e.PR <= 0 || e.Revision == 0 || e.Predecessor == ^ChangeRevision(0) || e.Revision != e.Predecessor+1 || strings.TrimSpace(e.HeadSHA) == "" || strings.TrimSpace(e.Basis.BaseSHA) == "" || e.Basis.At.IsZero() {
		return fmt.Errorf("invalid change revision observation")
	}
	return nil
}

// RevisionRecord retains the latest head even after closure/dequeue. ChangeID
// is absent for legacy observations and derived at read time from the queue key.
// LogPosition locates the revision boundary without inventing a source time.
type RevisionRecord struct {
	ChangeID    ChangeID
	Revision    ChangeRevision
	HeadSHA     string
	Basis       RevisionObservationBasis
	LogPosition uint64
}

// PRRevision returns recorded evidence by queue-local PR alias. It cannot
// manufacture accountability, classification, approvals, or source timestamps.
func (s State) PRRevision(pr int) (RevisionRecord, bool) {
	r, ok := s.changeRevisions.get(pr)
	return r, ok
}

// CanObserveChangeRevision rejects stale updates. A matching legacy boundary
// can be enriched once, so PRObserved and the new event can coexist in either
// order without counting one forge observation as two revisions.
func (s State) CanObserveChangeRevision(e ChangeRevisionObserved) bool {
	if e.Validate() != nil {
		return false
	}
	current, _ := s.PRRevision(e.PR)
	if current.ChangeID != "" && current.ChangeID != e.ChangeID {
		return false
	}
	if current.Revision == e.Revision && current.HeadSHA == e.HeadSHA && current.Basis.At.IsZero() && (current.Basis.BaseSHA == "" || current.Basis.BaseSHA == e.Basis.BaseSHA) {
		return true
	}
	return current.Revision == e.Predecessor && current.HeadSHA != e.HeadSHA
}

// ChangeRevisions returns detached current boundaries for ingestion recovery.
// It allocates nothing on the legacy planner path.
func (s State) ChangeRevisions() map[int]RevisionRecord { return s.changeRevisions.snapshot() }
