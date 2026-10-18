package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"go.yaml.in/yaml/v3"
)

type checkEvidence map[string]map[string]bool

func (c *setupAPI) importChecks(ctx context.Context, repo, sha string) (checkEvidence, error) {
	result := make(checkEvidence)
	add := func(name, producer string) {
		if result[name] == nil {
			result[name] = make(map[string]bool)
		}
		result[name][producer] = true
	}
	for page := 1; ; page++ {
		var checks struct {
			Runs []struct {
				Name string `json:"name"`
				App  struct {
					ID int64 `json:"id"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/commits/%s/check-runs?filter=all&per_page=100&page=%d", repo, url.PathEscape(sha), page), nil, &checks)
		if err != nil {
			return nil, c.importError(err, "Checks read")
		}
		for _, run := range checks.Runs {
			add(run.Name, fmt.Sprintf("check:%d", run.App.ID))
		}
		if !next {
			break
		}
	}
	for page := 1; ; page++ {
		var statuses []struct {
			Context string `json:"context"`
			Creator struct {
				ID int64 `json:"id"`
			} `json:"creator"`
		}
		next, err := c.page(ctx, http.MethodGet, fmt.Sprintf("repos/%s/commits/%s/statuses?per_page=100&page=%d", repo, url.PathEscape(sha), page), nil, &statuses)
		if err != nil {
			return nil, c.importError(err, "Commit statuses read")
		}
		for _, status := range statuses {
			add(status.Context, fmt.Sprintf("status:%d", status.Creator.ID))
		}
		if !next {
			break
		}
	}
	return result, nil
}

type importWorkflow struct {
	Path, Job, Name, Queue string
	PRonly                 bool
	Unresolved             bool
}

func (c *setupAPI) importWorkflows(ctx context.Context, repo string, tree importTree) ([]importWorkflow, error) {
	var paths []string
	for path := range tree {
		if strings.HasPrefix(path, ".github/workflows/") && !strings.Contains(strings.TrimPrefix(path, ".github/workflows/"), "/") && (strings.HasSuffix(path, ".yml") || strings.HasSuffix(path, ".yaml")) {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	var result []importWorkflow
	for _, path := range paths {
		data, err := c.importBlob(ctx, repo, tree[path])
		if err != nil {
			return nil, err
		}
		var doc struct {
			On   yaml.Node `yaml:"on"`
			Jobs map[string]struct {
				Name     string    `yaml:"name"`
				Strategy yaml.Node `yaml:"strategy"`
				Uses     string    `yaml:"uses"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("cannot parse workflow %s: %w", path, err)
		}
		queue, prOnly := workflowQueue(doc.On)
		keys := make([]string, 0, len(doc.Jobs))
		for key := range doc.Jobs {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			job := doc.Jobs[key]
			name := job.Name
			if name == "" {
				name = key
			}
			if strings.Contains(name, "${{") || job.Strategy.Kind != 0 || job.Uses != "" {
				result = append(result, importWorkflow{Path: path, Job: key, Name: name, Queue: "unknown", Unresolved: true})
				continue
			}
			result = append(result, importWorkflow{Path: path, Job: key, Name: name, Queue: queue, PRonly: prOnly})
		}
	}
	return result, nil
}
func workflowQueue(on yaml.Node) (string, bool) {
	events := map[string]*yaml.Node{}
	switch on.Kind {
	case yaml.ScalarNode:
		events[on.Value] = nil
	case yaml.SequenceNode:
		for _, node := range on.Content {
			events[node.Value] = nil
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			events[on.Content[i].Value] = on.Content[i+1]
		}
	default:
		return "unknown", false
	}
	if _, reusable := events["workflow_call"]; reusable {
		return "unknown (reusable workflow caller chain unresolved)", false
	}
	push, hasPush := events["push"]
	if !hasPush {
		_, pr := events["pull_request"]
		_, target := events["pull_request_target"]
		return "no", (pr || target) && len(events) == 1
	}
	if push == nil || push.Tag == "!!null" {
		return "yes", false
	}
	if push.Kind != yaml.MappingNode {
		return "unknown", false
	}
	values := map[string]*yaml.Node{}
	for i := 0; i+1 < len(push.Content); i += 2 {
		values[push.Content[i].Value] = push.Content[i+1]
	}
	if values["paths"] != nil || values["paths-ignore"] != nil {
		return "unknown (path filters)", false
	}
	if ignores := values["branches-ignore"]; ignores != nil {
		if ignores.Kind != yaml.SequenceNode {
			return "unknown", false
		}
		for _, n := range ignores.Content {
			if queuePatternCovers(n.Value) {
				return "no", false
			}
		}
		return "unknown (branch exclusions)", false
	}
	branches := values["branches"]
	if branches == nil {
		if values["tags"] != nil || values["tags-ignore"] != nil {
			return "no", false
		}
		return "yes", false
	}
	if branches.Kind != yaml.SequenceNode {
		return "unknown", false
	}
	included := false
	possible := false
	for _, n := range branches.Content {
		if strings.HasPrefix(n.Value, "!") {
			return "unknown (ordered negative branch filters)", false
		}
		if queuePatternCovers(n.Value) {
			included = true
		}
		prefix := n.Value
		if i := strings.IndexAny(prefix, "*?+[{"); i >= 0 {
			prefix = prefix[:i]
		}
		if strings.HasPrefix(prefix, "openmerge/queue/") || strings.HasPrefix("openmerge/queue/", prefix) {
			possible = true
		}
	}
	if included {
		return "yes", false
	}
	if !possible {
		return "no", false
	}
	return "unknown (no proven coverage of all openmerge/queue/** refs)", false
}
func producerNames(producers map[string]bool) []string {
	names := make([]string, 0, len(producers))
	for name := range producers {
		names = append(names, name)
	}
	return names
}

func queuePatternCovers(pattern string) bool {
	return pattern == "**" || pattern == "openmerge/**" || pattern == "openmerge/queue/**"
}

func (r *ProtectionReport) requiredChecks(base, source string, p importStatusChecks, evidence checkEvidence, workflows []importWorkflow) {
	checks := append(slices.Clone(p.Checks), p.Required...)
	for _, context := range p.Contexts {
		if !slices.ContainsFunc(checks, func(c importRequiredCheck) bool { return c.Context == context }) {
			checks = append(checks, importRequiredCheck{Context: context})
		}
	}
	slices.SortFunc(checks, func(a, b importRequiredCheck) int { return strings.Compare(a.Context, b.Context) })
	occurrences := make(map[string]int)
	for _, check := range checks {
		occurrences[check.Context]++
	}
	seen := make(map[string]bool)
	for _, check := range checks {
		if seen[check.Context] {
			continue
		}
		seen[check.Context] = true
		producers := evidence[check.Context]
		class, message := "equivalent", "The observed name identifies one producer; merge_when uses the literal check name. Producer evidence is limited to the pinned base commit; re-audit after CI changes."
		if len(producers) != 1 || occurrences[check.Context] > 1 {
			class = "unsupported"
			message = "The required name does not identify exactly one observed producer (missing or ambiguous evidence, including duplicate requirements)."
		}
		pinned := check.AppID
		if pinned == nil {
			pinned = check.IntegrationID
		}
		if pinned != nil && *pinned > 0 && class != "unsupported" {
			class = "weaker"
			message = "The GitHub requirement is App-pinned; check-success does not bind an App identity. Explicit acceptance loses that guarantee."
		}
		var found []importWorkflow
		for _, w := range workflows {
			if w.Unresolved {
				class = "unsupported"
				message += fmt.Sprintf(" Unresolved producer candidate %s job %s name %q (dynamic name, matrix or reusable workflow); name collisions cannot be excluded.", w.Path, w.Job, w.Name)
			}
		}
		for _, w := range workflows {
			if w.Name == check.Context {
				found = append(found, w)
			}
		}
		queue := "unknown: no statically identified workflow; external CI, matrix names and reusable workflows need operator verification"
		if len(found) > 0 {
			queue = "no"
			onlyPR := true
			for _, w := range found {
				if w.Queue == "yes" {
					queue = "yes"
				} else if queue != "yes" && w.Queue != "no" {
					queue = w.Queue
				}
				onlyPR = onlyPR && w.PRonly
			}
			if queue == "no" && !onlyPR {
				class = "unsupported"
				message += " Producing workflows do not run on queue-ref pushes; CI changes are required."
			}
			if onlyPR {
				class = "unsupported"
				message += " Producing workflows run only on pull requests; add queue-ref push CI before migration."
			}
			hasStatus := slices.ContainsFunc(producerNames(producers), func(name string) bool { return strings.HasPrefix(name, "status:") })
			if hasStatus {
				class = "unsupported"
				message += " A commit status and a workflow check share the required name."
			}
			if len(found) > 1 {
				class = "unsupported"
				message += " Multiple workflow jobs produce this name."
			}
		}
		if queue != "yes" {
			class = "unsupported"
			message += " Queue-ref push coverage is not proven; resolve coverage or change CI before migration."
		}
		names := make([]string, 0, len(producers))
		for name := range producers {
			names = append(names, name)
		}
		slices.Sort(names)
		parameters := []string{}
		for _, requirement := range checks {
			if requirement.Context == check.Context {
				encoded, _ := json.Marshal(requirement)
				parameters = append(parameters, string(encoded))
			}
		}
		slices.Sort(parameters)
		message += " Required check parameters: [" + strings.Join(parameters, ",") + "]."
		message += fmt.Sprintf(" Producers observed: %s. Workflow push coverage for openmerge/queue/**: %s.", strings.Join(names, ", "), queue)
		for _, w := range found {
			message += fmt.Sprintf(" %s job %s: queue pushes=%s.", w.Path, w.Job, w.Queue)
		}
		term := "check-success = " + check.Context
		frag := "# Cannot generate a safe literal check condition for this name.\n"
		if _, err := condition.Parse(term); err == nil && strings.TrimSpace(check.Context) == check.Context && !strings.ContainsAny(check.Context, "\r\n") && check.Context != "" {
			if class != "unsupported" {
				if r.queueDrafts == nil {
					r.queueDrafts = map[string]importQueueDraft{}
				}
				draft := r.queueDrafts[base]
				draft.Checks = append(draft.Checks, term)
				r.queueDrafts[base] = draft
				frag = "# merge_when term " + strconv.Quote(term) + " belongs in this base's reconciled queue fragment.\n"
			} else {
				frag = "# No equivalent merge_when term until this check requirement is resolved.\n"
			}
		} else {
			class = "unsupported"
			message += " The check name cannot be represented literally in the condition grammar."
		}
		r.add(base, source, "check-"+check.Context, class, false, message, frag)
		r.add(base, source, "result-semantics:"+check.Context, "stricter", false, "OpenMerge counts neutral and skipped results as failures; GitHub accepts them. This may stall changes GitHub would merge.", "# check "+strconv.Quote(check.Context)+": only success counts, never neutral/skipped.")
	}
	if p.Strict || p.StrictPolicy {
		if r.queueDrafts == nil {
			r.queueDrafts = map[string]importQueueDraft{}
		}
		draft := r.queueDrafts[base]
		draft.Strict = true
		r.queueDrafts[base] = draft
		r.add(base, source, "up-to-date", "equivalent", false, "GitHub strict up-to-date checks map to strict verification: the landed commit is the tested commit. verification: post_merge would be a weaker relaxation and requires a separately reviewed policy change under ADR 0005.", "# verification: strict belongs in this base's reconciled queue fragment.\n# verification: post_merge relaxes strict up-to-date protection.\n")
	}
}
