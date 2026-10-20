package forge

import (
	"fmt"
	"slices"
	"strings"
)

// PushOperation identifies today's five local Git mutation families. API
// DeleteRef cleanup of disposable queue test refs is deliberately excluded.
type PushOperation string

// Supported operations and hard prepared-inventory budgets.
const (
	PushBuild            PushOperation = "build"
	PushLand             PushOperation = "land"
	PushEnsureFence      PushOperation = "ensure_fence"
	PushAdvanceFence     PushOperation = "advance_fence"
	PushDeleteBranch     PushOperation = "delete_branch"
	MaxPushRefs                        = 2
	MaxPushCommits                     = 10_000
	MaxPushPaths                       = 100_000
	MaxPushMetadataBytes               = 16 << 20
)

// PushFile is one entry of the complete tree of an introduced commit. Blob
// content need not be retained; paths, object types and exact byte sizes do.
// Submodules have Kind commit and Size zero; symlinks are ordinary blobs.
type PushFile struct {
	Path, OID, Kind string
	Size            int64
}

// PushCommit describes an actual prepared object, never a reconstructed preview.
// SignatureVerified requires cryptographic/trust proof; merely finding a
// gpgsig header is not verification. Today's generated objects are unsigned.
type PushCommit struct {
	OID               string
	Parents           []string
	Message           string
	Signature         string
	SignatureVerified bool
	Files             []PushFile
}

// PushRef includes the exact old/new object IDs (empty denotes absence), every
// introduced reachable commit, and the frontier proven reachable from OldSHA.
// Producers must inspect locally under their existing session/cache lock, bound
// enumeration, reject shallow/incomplete graphs, and prove every Boundary entry
// reachable from OldSHA. Deletions introduce no commits and have no boundary.
type PushRef struct {
	Ref, OldSHA, NewSHA string
	Commits             []PushCommit
	Boundary            []string
}

// PreparedPush is complete only after all refs and reachable introduced objects
// have been inspected. No remote post-publication reader can produce it safely.
type PreparedPush struct {
	Operation PushOperation
	Refs      []PushRef
	Complete  bool
}

// ValidatePreparedPush enforces shared fake/real completeness and bounds. The
// producer owns the old-reachability proofs; this checks closed graph accounting.
func ValidatePreparedPush(p PreparedPush) error {
	switch p.Operation {
	case PushBuild, PushLand, PushEnsureFence, PushAdvanceFence, PushDeleteBranch:
	default:
		return invalid("unknown push operation")
	}
	if !p.Complete || len(p.Refs) < 1 || len(p.Refs) > MaxPushRefs || (p.Operation == PushLand) != (len(p.Refs) == 2) {
		return invalid("incomplete prepared push refs")
	}
	refs := map[string]bool{}
	count, paths, metadata := 0, 0, 0
	for _, r := range p.Refs {
		if !strings.HasPrefix(r.Ref, "refs/heads/") || ValidateRefName(r.Ref) != nil || refs[r.Ref] || r.OldSHA == "" && r.NewSHA == "" {
			return invalid("invalid prepared ref")
		}
		refs[r.Ref] = true
		for _, oid := range []string{r.OldSHA, r.NewSHA} {
			if oid != "" && ValidateCommitIDs(oid) != nil {
				return invalid("invalid prepared ref OID")
			}
		}
		if r.Commits == nil || r.Boundary == nil {
			return invalid("missing prepared graph")
		}
		if r.NewSHA == "" && (len(r.Commits) != 0 || len(r.Boundary) != 0) {
			return invalid("deletion introduces objects")
		}
		graph := map[string]PushCommit{}
		boundary := map[string]bool{}
		for _, oid := range r.Boundary {
			if r.OldSHA == "" || ValidateCommitIDs(oid) != nil || boundary[oid] {
				return invalid("invalid old graph boundary")
			}
			boundary[oid] = true
		}
		for _, c := range r.Commits {
			count++
			metadata += len(c.Message) + len(c.Signature)
			if count > MaxPushCommits || metadata > MaxPushMetadataBytes {
				return invalid("prepared push exceeds object budget")
			}
			if ValidateCommitIDs(c.OID) != nil || graph[c.OID].OID != "" || boundary[c.OID] || c.Parents == nil || c.Files == nil {
				return invalid("incomplete or duplicate prepared commit")
			}
			for _, parent := range c.Parents {
				if ValidateCommitIDs(parent) != nil {
					return invalid("invalid prepared parent")
				}
			}
			seen := map[string]bool{}
			for _, f := range c.Files {
				paths++
				metadata += len(f.Path)
				if paths > MaxPushPaths || metadata > MaxPushMetadataBytes {
					return invalid("prepared push exceeds path budget")
				}
				if f.Path == "" || strings.HasPrefix(f.Path, "/") || strings.ContainsRune(f.Path, 0) || seen[f.Path] || ValidateCommitIDs(f.OID) != nil || f.Size < 0 || f.Kind != "blob" && f.Kind != "commit" || f.Kind == "commit" && f.Size != 0 {
					return invalid("invalid prepared file")
				}
				seen[f.Path] = true
			}
			graph[c.OID] = c
		}
		seen, active, usedBoundary := map[string]bool{}, map[string]bool{}, map[string]bool{}
		var visit func(string) error
		visit = func(oid string) error {
			if boundary[oid] {
				usedBoundary[oid] = true
				return nil
			}
			if active[oid] {
				return invalid("cyclic prepared graph")
			}
			if seen[oid] {
				return nil
			}
			c, ok := graph[oid]
			if !ok {
				return invalid("missing introduced parent or tip")
			}
			active[oid] = true
			for _, parent := range c.Parents {
				if err := visit(parent); err != nil {
					return err
				}
			}
			delete(active, oid)
			seen[oid] = true
			return nil
		}
		if r.NewSHA != "" {
			if err := visit(r.NewSHA); err != nil {
				return err
			}
		}
		if len(seen) != len(graph) || len(usedBoundary) != len(boundary) {
			return invalid("unreachable prepared evidence")
		}
	}
	return nil
}

// PushRefIntent binds an operation's exact refs and leases before objects exist.
// A nonempty NewSHA additionally pins a known landing object; Delete explicitly
// distinguishes deletion from a not-yet-built generated object.
type PushRefIntent struct {
	Ref, OldSHA, NewSHA string
	Delete              bool
}

// MatchesPreparedRefs checks leases and optional known new OIDs as a set.
func MatchesPreparedRefs(intents []PushRefIntent, refs []PushRef) bool {
	if len(intents) != len(refs) {
		return false
	}
	seen := map[string]bool{}
	for _, i := range intents {
		if seen[i.Ref] {
			return false
		}
		seen[i.Ref] = true
		n := slices.IndexFunc(refs, func(r PushRef) bool { return r.Ref == i.Ref })
		if n < 0 {
			return false
		}
		r := refs[n]
		if i.OldSHA != r.OldSHA || i.Delete != (r.NewSHA == "") || i.NewSHA != "" && i.NewSHA != r.NewSHA {
			return false
		}
	}
	return true
}

func pushRejected(message string) error {
	return fmt.Errorf("push validation: %s: %w", message, ErrInvalidInput)
}
