// Package journal defines inert ADR 0010 event contracts and pure replay.
// No live journal reader, writer, queue, or store imports this package.
package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	p "github.com/CanadaApollo6/openmerge/internal/provenance"
)

// WireVersion is independent of the frozen queue event schema.
const WireVersion = 1
const (
	maxEventBytes    = 1 << 20
	maxSnapshotBytes = 64 << 20
	maxRecords       = 4096
	maxChunkCommits  = 128
)

// Replay errors never modify the input state.
var (
	ErrMalformed  = errors.New("malformed provenance evidence")
	ErrLimit      = errors.New("provenance evidence limit exceeded")
	ErrBinding    = errors.New("provenance generation binding mismatch")
	ErrIncomplete = errors.New("incomplete provenance inventory")
)

// Event contains only new kinds. It is deliberately separate from queue.Event.
type Event interface {
	Validate() error
	eventKind() string
}

// PolicySource identifies the governing default-branch read, not the change head.
// EffectiveDigest includes identity rules and requirements as well as ParserID.
type PolicySource struct {
	DefaultBranch   string `json:"default_branch"`
	ReadRef         string `json:"read_ref"`
	Commit          string `json:"commit"`
	EffectiveDigest string `json:"effective_digest"`
}

// Observation is one bounded chunk of a revision's source inventory. All fields
// except Index and Commits must be identical across chunks. Digest commits to
// the entire assembled Inventory, including diagnostic, limits and Git version.
// Generation is Request.Generation; it is unique per change/revision observation.
type Observation struct {
	PreviousObservationID uint64             `json:"previous_observation_id"`
	Request               p.InventoryRequest `json:"request"`
	Parser                p.ParserPolicy     `json:"parser"`
	ParserID              string             `json:"parser_id"`
	Policy                PolicySource       `json:"policy"`
	Limits                p.InventoryLimits  `json:"limits"`
	Stable                []string           `json:"stable"`
	Additional            []string           `json:"additional"`
	MergeBases            []string           `json:"merge_bases"`
	BuildMergeBases       []string           `json:"build_merge_bases"`
	Complete              bool               `json:"complete"`
	Diagnostic            p.Diagnostic       `json:"diagnostic"`
	DeclarationDigest     string             `json:"declaration_digest"`
	Digest                string             `json:"digest"`
	Index                 int                `json:"index"`
	Chunks                int                `json:"chunks"`
	Commits               []p.Commit         `json:"commits"`
}

func (Observation) eventKind() string { return "change_provenance_observed" }

// Inventory is the canonical record hashed by InventoryDigest. Commits must be
// sorted by original object ID, with sorted/deduplicated declaration sets.
type Inventory struct {
	Header  Observation `json:"header"`
	Commits []p.Commit  `json:"commits"`
}

// InventoryDigest normalizes only chunk framing, not caller-supplied set order.
// It is separate from slice 1's declaration digest and never changes receipts.
func InventoryDigest(header Observation, commits []p.Commit) string {
	header.Index, header.Chunks, header.Digest = 0, 0, ""
	header.Commits = []p.Commit{}
	return hash(Inventory{header, commits})
}

func hash(value any) string {
	raw, _ := json.Marshal(value)
	// Domain separation is part of evidence-record v1, not export v2 hashing.
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("openmerge-provenance-record-v1\x00"), raw...)))
}

func digest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && strings.ToLower(s) == s
}
func objectID(s string) bool { return digest(s) || len(s) == 40 && digest(s+strings.Repeat("0", 24)) }
func text(s string) bool {
	return s != "" && len(s) <= 256 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func ids(values []string, objects bool) bool {
	if values == nil {
		return false
	}
	for i, s := range values {
		if (objects && !objectID(s)) || (!objects && !text(s)) || i > 0 && values[i-1] >= s {
			return false
		}
	}
	return true
}
func requestValid(r p.InventoryRequest) bool {
	return text(r.Repository) && text(r.Queue) && text(r.ChangeID) && text(r.Revision) && text(r.Generation) && objectID(r.Head) && objectID(r.PinnedBase) && objectID(r.BuildBase) && ids(r.Excluded, true)
}
func diagnosticValid(complete bool, d p.Diagnostic) bool {
	if d.ObjectID != "" && !objectID(d.ObjectID) {
		return false
	}
	if complete {
		return d.Outcome == p.ReadComplete && d.Resource == "" && d.ObjectID == ""
	}
	return (d.Outcome == p.ReadPending || d.Outcome == p.LimitExceeded || d.Outcome == p.InvalidInput) && text(d.Resource)
}

// Validate checks chunk-local shape; Apply checks the full inventory and fences.
func (e Observation) Validate() error {
	if !requestValid(e.Request) || e.Parser != p.NewParserPolicy(e.Parser.GitVersion) || !text(e.Parser.GitVersion) || e.ParserID != e.Parser.Identity() || !text(e.Policy.DefaultBranch) || e.Policy.ReadRef != "refs/heads/"+e.Policy.DefaultBranch || !objectID(e.Policy.Commit) || !digest(e.Policy.EffectiveDigest) || e.Limits.Validate() != nil || !diagnosticValid(e.Complete, e.Diagnostic) || !digest(e.Digest) {
		return ErrMalformed
	}
	if e.Complete && !digest(e.DeclarationDigest) || !e.Complete && e.DeclarationDigest != "" {
		return ErrMalformed
	}
	if !ids(e.Stable, true) || !ids(e.Additional, true) || !ids(e.MergeBases, true) || !ids(e.BuildMergeBases, true) || len(e.Stable)+len(e.Additional) > e.Limits.SourceCommits || len(e.MergeBases)+len(e.BuildMergeBases) > e.Limits.GraphCommits {
		return ErrMalformed
	}
	if e.Chunks < 1 || e.Chunks > e.Limits.SourceCommits || e.Index < 0 || e.Index >= e.Chunks || len(e.Commits) > maxChunkCommits || e.Commits == nil || e.Chunks > 1 && len(e.Commits) == 0 {
		return ErrLimit
	}
	for i, c := range e.Commits {
		if !objectID(c.ObjectID) || i > 0 && e.Commits[i-1].ObjectID >= c.ObjectID || e.Complete && !c.Complete || c.Declarations == nil || len(c.Declarations) > e.Limits.Declarations {
			return ErrMalformed
		}
		// Reuse slice 1's normalized declaration validator even for pending reads.
		normalized := c
		normalized.Complete = true
		if _, err := p.CanonicalDeclarations([]p.Commit{normalized}, true, nil); err != nil {
			return ErrMalformed
		}
		for j, d := range c.Declarations {
			if j > 0 && declarationKey(c.Declarations[j-1]) >= declarationKey(d) {
				return ErrMalformed
			}
		}
	}
	return nil
}
func declarationKey(d p.Declaration) string {
	return fmt.Sprintf("%q", []string{d.Source, d.Version, d.Kind, d.Value, d.ValueDigest, string(d.Outcome), string(d.Class), d.RuleID})
}

// Input retains typed condition values, including unknown values, without
// extending the frozen queue condition-evidence vocabulary.
type Input struct {
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Known  bool     `json:"known"`
	String string   `json:"string"`
	Bool   bool     `json:"bool"`
	List   []string `json:"list"`
}

// Layer retains the independently evaluated baseline, trigger, or rule-local
// obligation. Eligibility and References record its own approval pool/evidence.
type Layer struct {
	Kind        string   `json:"kind"`
	Rule        string   `json:"rule"`
	Expression  string   `json:"expression"`
	Inputs      []Input  `json:"inputs"`
	Eligibility []string `json:"eligibility"`
	References  []uint64 `json:"references"`
	Truth       string   `json:"truth"`
}

// Member binds a single change/revision to every inventory chunk and one trusted
// composition. Tested and landed stages are distinct observations, never pooled.
type Member struct {
	Request           p.InventoryRequest `json:"request"`
	ObservationIDs    []uint64           `json:"observation_ids"`
	InventoryDigest   string             `json:"inventory_digest"`
	DeclarationDigest string             `json:"declaration_digest"`
	Mapping           p.BuildMapping     `json:"mapping"`
	Layers            []Layer            `json:"layers"`
	Result            string             `json:"result"`
	RecordDigest      string             `json:"record_digest"`
}

// MemberDigest covers mutable gate evidence separately from declaration identity.
func MemberDigest(m Member) string { m.RecordDigest = ""; return hash(m) }

// Landing is a companion to existing decision/check/LandingStarted events. The
// later writer must commit all chunk and decision bindings atomically. This
// contract records evidence; it does not authorize or append a landing.
type Landing struct {
	AttemptID  string   `json:"attempt_id"`
	BatchID    string   `json:"batch_id"`
	DecisionID uint64   `json:"decision_id"`
	Stage      string   `json:"stage"`
	Members    []Member `json:"members"`
}

func (Landing) eventKind() string { return "landing_provenance_recorded" }
func truth(s string) bool         { return s == "true" || s == "false" || s == "pending" }
func eventIDs(values []uint64) bool {
	if len(values) == 0 {
		return false
	}
	for i, v := range values {
		if v == 0 || i > 0 && values[i-1] >= v {
			return false
		}
	}
	return true
}

// Validate checks bounded member evidence; Apply resolves exact generation refs.
func (e Landing) Validate() error {
	if !text(e.AttemptID) || !text(e.BatchID) || e.DecisionID == 0 || e.Stage != "tested" && e.Stage != "landed" || len(e.Members) == 0 || len(e.Members) > 1024 {
		return ErrMalformed
	}
	seen := map[string]bool{}
	for _, m := range e.Members {
		key := m.Request.ChangeID
		if seen[key] || !requestValid(m.Request) || !eventIDs(m.ObservationIDs) || len(m.ObservationIDs) > 1024 || !digest(m.InventoryDigest) || !digest(m.DeclarationDigest) || m.RecordDigest != MemberDigest(m) || !truth(m.Result) || len(m.Layers) == 0 || len(m.Layers) > 256 {
			return ErrMalformed
		}
		seen[key] = true
		b := m.Mapping
		if !same(b.Request, m.Request) || !digest(b.PolicyID) || b.Build.Stage != e.Stage || !text(b.Build.ID) || !objectID(b.Build.Base) || !objectID(b.Build.Result) || b.Build.Base != m.Request.BuildBase || b.Build.Marker != "" && !objectID(b.Build.Marker) || !slices.Contains([]string{"merge", "squash", "rebase", "fast-forward"}, b.Build.Method) || !diagnosticValid(b.Complete, b.Diagnostic) {
			return ErrMalformed
		}
		if b.Commits == nil || b.Build.Replays == nil || len(b.Commits) > 1024 || len(b.Build.Replays) > 1024 {
			return ErrMalformed
		}
		for _, pairs := range [][]p.CommitMapping{b.Commits, b.Build.Replays} {
			for i, v := range pairs {
				if !objectID(v.Original) || !objectID(v.Build) || i > 0 && pairs[i-1].Original >= v.Original {
					return ErrMalformed
				}
			}
		}
		if m.Layers[0].Kind != "baseline" {
			return ErrMalformed
		}
		layerKeys := map[string]bool{}
		for i, l := range m.Layers {
			if !truth(l.Truth) || !ids(l.Eligibility, false) || l.Inputs == nil || l.References == nil || len(l.Inputs) > 256 || len(l.References) > 1024 || len(l.References) > 0 && !eventIDs(l.References) || !text(l.Expression) {
				return ErrMalformed
			}
			if i == 0 && l.Rule != "" || i > 0 && (l.Kind != "trigger" && l.Kind != "requirement" || !text(l.Rule)) {
				return ErrMalformed
			}
			lk := l.Kind + ":" + l.Rule
			if layerKeys[lk] {
				return ErrMalformed
			}
			layerKeys[lk] = true
			for j, v := range l.Inputs {
				if !text(v.Name) || j > 0 && l.Inputs[j-1].Name >= v.Name || !slices.Contains([]string{"string", "bool", "list"}, v.Type) || !ids(v.List, false) || len(v.String) > 4096 || !utf8.ValidString(v.String) || v.Type != "string" && v.String != "" || v.Type != "bool" && v.Bool || v.Type != "list" && len(v.List) > 0 || !v.Known && (v.String != "" || v.Bool || len(v.List) > 0) {
					return ErrMalformed
				}
			}
		}
		// Preserve trigger/obligation separation. Only a true baseline and each
		// (false trigger OR true obligation) can be recorded as a passing member.
		result := m.Layers[0].Truth
		for _, l := range m.Layers[1:] {
			if l.Kind == "requirement" && !layerKeys["trigger:"+l.Rule] {
				return ErrMalformed
			}
			if l.Kind != "trigger" {
				continue
			}
			required := ""
			for _, r := range m.Layers[1:] {
				if r.Kind == "requirement" && r.Rule == l.Rule {
					required = r.Truth
				}
			}
			if required == "" {
				return ErrMalformed
			}
			pass := "pending"
			if l.Truth == "false" || required == "true" {
				pass = "true"
			} else if l.Truth == "true" && required == "false" {
				pass = "false"
			}
			result = and(result, pass)
		}
		if m.Result != result || m.Result == "true" && !b.Complete {
			return ErrMalformed
		}
	}
	return nil
}
func and(a, b string) string {
	if a == "false" || b == "false" {
		return "false"
	}
	if a == "pending" || b == "pending" {
		return "pending"
	}
	return "true"
}
func same(a, b any) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
