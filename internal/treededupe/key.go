// Package treededupe computes ADR 0015 exact-tree cache keys and folds SHADOW
// observations of what an exact-tree dedupe cache would have reused. Slice 1
// only measures: no planner, executor, landing or published-check path reads
// anything from this package, so it cannot change a queue decision.
//
// A key binds the tested commit's Git TREE OID, the workflow file blob OIDs
// at that commit, a digest of the queue's required-check definition
// (merge_when) and an immutable runner-image identity, namespaced by the
// queue. Every input is required: an unknown or unreadable input makes the
// whole key unknown, and an unknown key is never a shadow hit (fail closed).
package treededupe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// KeyVersion is bound into every key digest. Changing key inputs or their
// encoding requires a new version so old and new keys can never collide.
const KeyVersion = "openmerge-tree-dedupe-key-v1"

// UnknownInput names one key input that was missing or unreadable.
type UnknownInput string

// Unknown inputs, reported in this order.
const (
	UnknownNamespace   UnknownInput = "namespace"
	UnknownTree        UnknownInput = "tree"
	UnknownWorkflows   UnknownInput = "workflows"
	UnknownChecks      UnknownInput = "checks"
	UnknownRunnerImage UnknownInput = "runner_image"
)

// WorkflowDirs are the workflow definition directories read at the tested
// commit. GitHub Actions reads only .github/workflows.
var WorkflowDirs = []string{".github/workflows"}

// Workflow is one `git ls-tree -r -z --full-tree` entry under WorkflowDirs.
type Workflow struct {
	Mode string `json:"mode"`
	Type string `json:"type"`
	OID  string `json:"oid"`
	Path string `json:"path"`
}

// GitInputs are read from the tested commit with Git's own readers
// (`git rev-parse <sha>^{tree}` and `git ls-tree`), never by hand-parsing
// objects. Callers must not synthesize them from forge APIs.
type GitInputs struct {
	Tree      string
	Workflows []Workflow
}

// Inputs are everything a key binds. A nil GitErr and nonempty fields are
// required for a known key.
type Inputs struct {
	// Namespace isolates tenants, repositories, base branches and lanes.
	Namespace string
	Git       GitInputs
	GitErr    error
	// Checks is the canonical required-check definition (the queue's
	// merge_when), or empty when the configuration is unavailable.
	Checks string
	// RunnerImage is an immutable runner image identity, or empty when unknown.
	// Mutable tags such as ubuntu-latest are not identities.
	RunnerImage string
}

// Key returns the hex key digest, or "" and every unknown input. A known key
// has no unknown inputs; a key with any unknown input is "" (fail closed).
func Key(in Inputs) (string, []UnknownInput) {
	var unknown []UnknownInput
	if in.Namespace == "" {
		unknown = append(unknown, UnknownNamespace)
	}
	if in.GitErr != nil || !validObjectID(in.Git.Tree) {
		unknown = append(unknown, UnknownTree)
	}
	workflows, ok := canonicalWorkflows(in.Git.Workflows)
	if in.GitErr != nil || !ok {
		unknown = append(unknown, UnknownWorkflows)
	}
	if strings.TrimSpace(in.Checks) == "" {
		unknown = append(unknown, UnknownChecks)
	}
	if !validRunnerImage(in.RunnerImage) {
		unknown = append(unknown, UnknownRunnerImage)
	}
	if len(unknown) > 0 {
		return "", unknown
	}
	checks := sha256.Sum256([]byte(in.Checks))
	preimage, err := json.Marshal(struct {
		Version     string     `json:"version"`
		Namespace   string     `json:"namespace"`
		Tree        string     `json:"tree"`
		Workflows   []Workflow `json:"workflows"`
		Checks      string     `json:"checks"`
		RunnerImage string     `json:"runner_image"`
	}{KeyVersion, in.Namespace, in.Git.Tree, workflows, hex.EncodeToString(checks[:]), in.RunnerImage})
	if err != nil {
		return "", []UnknownInput{UnknownWorkflows}
	}
	sum := sha256.Sum256(preimage)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalWorkflows requires at least one regular-file blob, valid lowercase
// object IDs and unique paths, and returns the entries sorted by path. A
// commit with no workflow files has no workflow identity: CI may come from
// elsewhere, so the key is unknown rather than vacuously equal.
func canonicalWorkflows(entries []Workflow) ([]Workflow, bool) {
	if len(entries) == 0 {
		return nil, false
	}
	out := slices.Clone(entries)
	slices.SortFunc(out, func(a, b Workflow) int { return strings.Compare(a.Path, b.Path) })
	for i, e := range out {
		if e.Type != "blob" || e.Mode != "100644" && e.Mode != "100755" || !validObjectID(e.OID) || !underWorkflowDir(e.Path) || strings.ContainsAny(e.Path, "\x00\n") {
			return nil, false
		}
		if i > 0 && out[i-1].Path == e.Path {
			return nil, false
		}
	}
	return out, true
}

func underWorkflowDir(path string) bool {
	for _, dir := range WorkflowDirs {
		if strings.HasPrefix(path, dir+"/") && len(path) > len(dir)+1 {
			return true
		}
	}
	return false
}

// validObjectID accepts Git's canonical lowercase SHA-1 or SHA-256 hex.
func validObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validRunnerImage rejects empty, whitespace-bearing and obviously mutable
// identities. The activation slice must supply a real immutable identity.
func validRunnerImage(image string) bool {
	if image == "" || len(image) > 512 || strings.ContainsFunc(image, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return false
	}
	return !strings.Contains(strings.ToLower(image), "latest")
}
