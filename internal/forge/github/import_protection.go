package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ImportOptions selects a read-only protection audit. Bases add concrete branches;
// they never narrow the queues discovered from default-branch configuration.
type ImportOptions struct {
	Repo   string
	Bases  []string
	Accept []string
	JSON   bool
	Out    string
}

// ProtectionItem IDs include the base, source, classification and requirement
// details, so acceptance cannot silently carry over to a changed requirement.
// Blocking installation prerequisites cannot be accepted.
type ProtectionItem struct {
	ID             string `json:"id"`
	Base           string `json:"base,omitempty"`
	Source         string `json:"source"`
	Classification string `json:"classification"`
	Blocking       bool   `json:"blocking"`
	Accepted       bool   `json:"accepted"`
	Message        string `json:"message"`
	Fragment       string `json:"fragment"`
}

// ProtectionReport is the versioned machine-readable report, including the draft
// and guide so --json loses none of the human output.
type ProtectionReport struct {
	Version           int              `json:"version"`
	Repository        string           `json:"repository"`
	DefaultBranch     string           `json:"default_branch"`
	GoverningCommit   string           `json:"governing_commit"`
	Bases             []string         `json:"bases"`
	Items             []ProtectionItem `json:"items"`
	ProposedConfig    string           `json:"proposed_config"`
	ContributingGuide string           `json:"contributing_guide"`
	Ready             bool             `json:"ready"`
	queueDrafts       map[string]importQueueDraft
	principals        map[string][]string
}

type importQueueDraft struct {
	Count        int
	Writers      []string
	PullRequests bool
	Strict       bool
	Checks       []string
}

const importGuide = `This repository merges through OpenMerge. Submit a pull request and use
@openmerge queue; follow its queue state instead of GitHub's merge button.
Disable GitHub auto-merge. CI must run on pushes to openmerge/queue/**.
Creating a branch matching a queue's base selector requires the OpenMerge App
or the declared break-glass team/repository-admin role. Break-glass merges are
recorded as outside-queue landings. Bots cannot be the break-glass actor.`

const stageA = "Requires the release implementing ADR 0007 Stage A; this is a proposed fragment, not configuration current releases can load."

// getOnlyTransport is installed outside authentication/cache transports, so even
// an accidental reuse of a setup mutation cannot reach GitHub (or its cache).
type getOnlyTransport struct{ base http.RoundTripper }

func (t getOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, errors.New("import-protection is read-only: non-GET request refused")
	}
	return t.base.RoundTrip(r)
}

// ImportProtection reports GitHub policy without changing repository or database
// state. The caller may save ProposedConfig locally after the complete read.
func ImportProtection(ctx context.Context, opts ImportOptions, token string) (ProtectionReport, error) {
	return importProtection(ctx, opts, token, "https://api.github.com/")
}

func importProtection(ctx context.Context, opts ImportOptions, token, api string) (ProtectionReport, error) {
	if _, _, err := splitRepoPath(opts.Repo); err != nil {
		return ProtectionReport{}, err
	}
	if token == "" {
		return ProtectionReport{}, errors.New("GITHUB_TOKEN is required: use a repository admin token with Administration read, Contents read, Checks read and Commit statuses read; inherited rulesets may require an organization owner")
	}
	c, err := setupClient(api, token)
	if err != nil {
		return ProtectionReport{}, err
	}
	c.http.Transport = getOnlyTransport{base: c.http.Transport}
	report, err := c.readProtection(ctx, opts)
	if err != nil {
		return ProtectionReport{}, err
	}
	if err := report.accept(opts.Accept); err != nil {
		return ProtectionReport{}, err
	}
	return report, nil
}

func (r *ProtectionReport) add(base, source, key, class string, blocking bool, message, fragment string) {
	sum := sha256.Sum256([]byte(base + "\x00" + source + "\x00" + key + "\x00" + class + "\x00" + message + "\x00" + fragment))
	r.Items = append(r.Items, ProtectionItem{ID: fmt.Sprintf("%s-%x", importIDPrefix(key), sum[:8]), Base: base, Source: source, Classification: class, Blocking: blocking, Message: message, Fragment: fragment})
}

func importIDPrefix(key string) string {
	return strings.Map(func(ch rune) rune {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-._:/", ch) {
			return ch
		}
		return '_'
	}, key)
}

func (r *ProtectionReport) accept(ids []string) error {
	requested := make(map[string]bool)
	for _, id := range ids {
		requested[id] = true
	}
	for i := range r.Items {
		item := &r.Items[i]
		if !requested[item.ID] {
			continue
		}
		if item.Blocking || item.Classification != "weaker" && item.Classification != "unsupported" {
			return fmt.Errorf("--accept %s is not an acceptable policy relaxation", item.ID)
		}
		item.Accepted = true
		delete(requested, item.ID)
	}
	if len(requested) > 0 {
		keys := make([]string, 0, len(requested))
		for id := range requested {
			keys = append(keys, id)
		}
		slices.Sort(keys)
		return fmt.Errorf("unknown --accept item IDs: %s", strings.Join(keys, ", "))
	}
	r.Ready = true
	var draft strings.Builder
	fmt.Fprintf(&draft, "# %s\n# Merge these fragments into the default-branch configuration; retain existing queue settings.\n", stageA)
	for _, item := range r.Items {
		if item.Blocking || (item.Classification == "weaker" || item.Classification == "unsupported") && !item.Accepted {
			r.Ready = false
		}
		fmt.Fprintf(&draft, "\n# %s [%s] base=%q source=%q\n", item.ID, item.Classification, item.Base, item.Source)
		if item.Accepted {
			fmt.Fprintf(&draft, "# ACCEPTED %s: %s\n", item.ID, strings.ReplaceAll(item.Message, "\n", " "))
		}
		draft.WriteString(item.Fragment)
		draft.WriteByte('\n')
	}
	bases := make([]string, 0, len(r.queueDrafts))
	for base := range r.queueDrafts {
		bases = append(bases, base)
	}
	slices.Sort(bases)
	for _, base := range bases {
		p := r.queueDrafts[base]
		fmt.Fprintf(&draft, "\n# Reconciled queue fragment for base=%q: combine classic protection and all applicable rulesets.\n# changes, approvals, verification and merge_when belong in this base's queue.\n", base)
		value := map[string]any{}
		if p.PullRequests {
			value["changes"] = map[string]any{"pull_requests": "required"}
		}
		if p.Strict {
			value["verification"] = "strict"
		}
		if len(p.Checks) > 0 {
			checks := slices.Clone(p.Checks)
			slices.Sort(checks)
			value["merge_when"] = slices.Compact(checks)
		}
		if p.Count > 0 {
			value["groups"] = map[string]any{"imported_reviewers": p.Writers}
			requirement := map[string]any{"group": "imported_reviewers", "count": p.Count}
			value["approvals"] = map[string]any{
				"default": requirement,
				"rules": []any{map[string]any{
					"when":    map[string]any{"paths": []string{"**"}},
					"require": requirement,
				}},
			}
		}
		draft.WriteString(fragment(value))
		if p.Count > 0 {
			draft.WriteString("# Maximum approval count across sources; groups is repository-level.\n# Keep both requirements: paths ** is unknown on empty diffs and enforced as if matched, plus the count-bearing default.\n# A definitely matching rule suppresses the default; additive rules still enforce the paths count, even with none.\n# Maintain imported_reviewers as writer membership changes; eligibility is re-read at landing.\n")
		}
	}
	r.ProposedConfig = draft.String()
	return nil
}

// Write emits only a report. A failing readiness result still has a complete
// report; permission/read failures never return a partial report to render.
func (r ProtectionReport) Write(out io.Writer, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(r)
	}
	var b strings.Builder
	blockers, gaps := 0, 0
	for _, item := range r.Items {
		if item.Blocking {
			blockers++
		} else if !item.Accepted && (item.Classification == "unsupported" || item.Classification == "weaker") {
			gaps++
		}
	}
	fmt.Fprintf(&b, "Ready: %t; installation/authority blockers: %d; unaccepted policy gaps: %d\n", r.Ready, blockers, gaps)
	fmt.Fprintf(&b, "Repository %s; default %s at %s; bases: %s\n%s\n", r.Repository, r.DefaultBranch, r.GoverningCommit, strings.Join(r.Bases, ", "), stageA)
	for _, item := range r.Items {
		flag := ""
		if item.Blocking {
			flag = " BLOCKER"
		}
		if item.Accepted {
			flag = " ACCEPTED"
		}
		fmt.Fprintf(&b, "\n[%s%s] %s (%s, %s)\n%s\n%s\n", item.Classification, flag, item.ID, item.Base, item.Source, item.Message, item.Fragment)
	}
	fmt.Fprintf(&b, "\nProposed .openmerge.yml fragments:\n%s\nContributing guide:\n%s\n", r.ProposedConfig, r.ContributingGuide)
	_, err := io.WriteString(out, b.String())
	return err
}

func fragment(value any) string { data, _ := yaml.Marshal(value); return string(data) }
