package provenance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// ReadOutcome distinguishes incomplete observations from terminal work limits.
type ReadOutcome string

// Local read outcomes are independent of checkpoint resolution outcomes.
const (
	ReadComplete  ReadOutcome = "complete"
	ReadPending   ReadOutcome = "pending"
	LimitExceeded ReadOutcome = "limit_exceeded"
	InvalidInput  ReadOutcome = "invalid_input"
)

// Diagnostic contains bounded machine-readable status, never Git stderr or messages.
type Diagnostic struct {
	Outcome  ReadOutcome
	Resource string
	ObjectID string
}

// InventoryLimits can lower process ceilings, but cannot raise them. CommitBytes
// bounds the entire raw object, including headers. MessageBytes is cumulative.
type InventoryLimits struct {
	SourceCommits, GraphCommits, GraphBytes, CommitBytes, MessageBytes, Declarations int
	Duration                                                                         time.Duration
}

// DefaultInventoryLimits includes a separate ceiling on ancestry exploration.
// The ADR does not specify local message/graph ceilings; these conservative
// defaults bound local reads independently of the later network resolver.
func DefaultInventoryLimits() InventoryLimits {
	return InventoryLimits{SourceCommits: 1024, GraphCommits: 16384, GraphBytes: 128 << 20, CommitBytes: 1 << 20, MessageBytes: 16 << 20, Declarations: 16, Duration: 30 * time.Second}
}

// Validate requires every budget explicitly, within the process ceilings.
func (l InventoryLimits) Validate() error {
	ceilings := DefaultInventoryLimits()
	if l.SourceCommits <= 0 || l.SourceCommits > ceilings.SourceCommits || l.GraphCommits <= 0 || l.GraphCommits > ceilings.GraphCommits ||
		l.GraphBytes <= 0 || l.GraphBytes > ceilings.GraphBytes || l.CommitBytes <= 0 || l.CommitBytes > ceilings.CommitBytes || l.MessageBytes <= 0 || l.MessageBytes > ceilings.MessageBytes ||
		l.Declarations <= 0 || l.Declarations > ceilings.Declarations || l.Duration <= 0 || l.Duration > ceilings.Duration {
		return fmt.Errorf("inventory limits must be positive and cannot raise process ceilings")
	}
	return nil
}

// ParserPolicy binds the deployed Git implementation and every fixed parsing
// choice. This identity belongs in the future effective policy digest.
type ParserPolicy struct {
	GitVersion, ParserVersion, ClassifierVersion string
	Invocation, Isolation                        string
}

// NewParserPolicy describes the isolated adapter, not source-repository config.
func NewParserPolicy(gitVersion string) ParserPolicy {
	return ParserPolicy{gitVersion, ParserVersion, ClassifierVersion,
		"interpret-trailers --parse --no-divider; trailer.separators=:; core.commentChar=#; core.commentString=#",
		"git-env-scrub-v1; system/global=disabled; template-free-bare-repository"}
}

// Identity is a stable hash of parser policy evidence, including Git version.
func (p ParserPolicy) Identity() string {
	b, _ := json.Marshal(p) // Only strings: marshaling cannot fail.
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// InventoryRequest identifies one change revision and one source observation.
// Excluded contains trusted exact IDs of synthetic markers or other members'
// originals. It must never be derived from contributor OpenMerge-* trailers.
type InventoryRequest struct {
	Repository, Queue, ChangeID, Revision, Generation string
	Head, PinnedBase, BuildBase                       string
	Excluded                                          []string
}

// Inventory retains normalized declarations and original IDs, not messages.
// Stable and Additional partition the union used by Commits. A failed inventory
// must be passed to declaration evaluation with Complete=false.
type Inventory struct {
	Request                                         InventoryRequest
	Policy                                          ParserPolicy
	Limits                                          InventoryLimits
	PolicyID                                        string
	Stable, Additional, MergeBases, BuildMergeBases []string
	Commits                                         []Commit
	Complete                                        bool
	Diagnostic                                      Diagnostic
}
