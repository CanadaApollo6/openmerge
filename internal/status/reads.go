package status

import (
	"net/url"
	"strings"
)

// Parameter keeps tool inputs explicit so discovery needs no route-specific knowledge.
type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// Read is the shared contract for discovery and future MCP tools.
type Read struct {
	Name        string      `json:"name"`
	Path        string      `json:"path"`
	Parameters  []Parameter `json:"parameters"`
	Description string      `json:"description"`
}

var queueParameters = []Parameter{
	{"owner", "path", "string", true, "Repository owner. Repository matching is case-insensitive."},
	{"repo", "path", "string", true, "Repository name."},
	{"base", "path", "string", true, "Exact raw base branch name, including slashes."},
}

var changeParameter = Parameter{"id", "path", "string", true, "Canonical change ID recorded in this queue."}
var revisionParameter = Parameter{"revision", "query", "integer", false, "Positive revision number. Omit for the current revision."}

func parameters(extra ...Parameter) []Parameter {
	return append(append([]Parameter{}, queueParameters...), extra...)
}

var reads = []Read{
	{"reads", "/v1", []Parameter{}, "Discover the versioned reads, their parameters, and their descriptions."},
	{"discovery", "/v1/discovery", []Parameter{}, "List discovered repositories, serving state, sweeps, and installation budgets."},
	{"queues", "/v1/queues", []Parameter{}, "List all queues served by this API, including their installation and lane."},
	{"repository_queues", "/v1/queues/{owner}/{repo}", queueParameters[:2], "List all served queues for a repository."},
	{"queue", "/v1/queues/{owner}/{repo}/{base}", parameters(), "Read queue state, entries, readiness, and CI history. Ambiguous installations or lanes return 409."},
	{"pr", "/v1/queues/{owner}/{repo}/{base}/prs/{n}", parameters(
		Parameter{"n", "path", "integer", true, "Positive pull request number."},
		Parameter{"events", "query", "integer", false, "Recent relevant events, from 1 to 100. Defaults to 20."}), "Read a pull request's queue state and waiting reason, with bounded event history."},
	{"change", "/v1/queues/{owner}/{repo}/{base}/changes/{id}", parameters(changeParameter, revisionParameter), "Read a change's recorded evidence, accountability, waiting reasons, and landing."},
	{"brief", "/v1/queues/{owner}/{repo}/{base}/changes/{id}/brief", parameters(changeParameter,
		Parameter{"audience", "query", "string", true, "Brief audience. One of reviewer, operator, or agent."}, revisionParameter), "Read the public recorded brief for a change and audience."},
	{"decisions", "/v1/queues/{owner}/{repo}/{base}/changes/{id}/decisions", parameters(changeParameter, revisionParameter,
		Parameter{"after", "query", "integer", false, "Nonnegative event cursor. Defaults to 0."},
		Parameter{"limit", "query", "integer", false, "Maximum decisions, from 1 to 100. Defaults to 20."}), "Read a page of recorded landing decisions for a change."},
	{"queue_events", "/v1/queues/{owner}/{repo}/{base}/events", parameters(
		Parameter{"after", "query", "integer", false, "Nonnegative journal event cursor. Defaults to 0."},
		Parameter{"limit", "query", "integer", false, "Maximum events, from 1 to 100. Defaults to 20."}), "Poll with after=next_cursor to resume queue event history after a restart. Drain pages while has_more is true, then wait at least 60 seconds before polling from the last cursor. Every page loads and replays the full journal on the shared worker pool, regardless of limit. Use one poller per queue and back off after transient 503 unavailable. A 409 event-history-withheld means positions are private. Retrying will not help. Ask an operator to restore access for retained history; use queue or PR detail meanwhile."},
	{"savings", "/v1/queues/{owner}/{repo}/{base}/reports/savings", parameters(
		Parameter{"since", "query", "string", true, "Inclusive window start in RFC3339 format."},
		Parameter{"until", "query", "string", false, "Exclusive window end in RFC3339 format. Defaults to now. Future times are rejected."},
		Parameter{"full-suite-minutes", "query", "number", false, "Finite nonnegative minutes for one full CI run. Zero or omitted uses the observed median."}), "Measure CI runs, CI minutes, and waiting saved from the journal. Requires an unambiguous served queue. Large histories may exceed the 15 second deadline."},
	{"health", "/v1/health", []Parameter{}, "Read bounded runtime readiness and liveness observations with bearer authentication."},
	{"authority_completion", "/v1/authority-completion", []Parameter{
		{"installation", "query", "string", true, "Installation identifier."},
		{"repo", "query", "string", true, "Repository in owner/name form."},
		{"digest", "query", "string", false, "Expected repository authority digest. Required unless targets supplies expected digests."},
		{"targets", "query", "string", false, "JSON object mapping base branches to expected digests. At most 1 MiB and 1000 targets."},
	}, "Read repository-wide authority completion against expected digests. Missing complete discovery evidence fails closed."},
	{"live", "/v1/live", []Parameter{}, "Probe process liveness without authentication. Returns only live, with no version envelope. Accepts no query or body."},
}

// Reads returns an independent copy for discovery and future tool generation.
func Reads() []Read {
	result := append([]Read{}, reads...)
	for i := range result {
		result[i].Parameters = append([]Parameter{}, result[i].Parameters...)
	}
	return result
}

// registeredRead runs after the route parsers, so their validation errors keep
// their contract while a new parser branch cannot serve a read without a row.
func registeredRead(escapedPath string) bool {
	parts := strings.Split(escapedPath, "/")
	for _, read := range reads {
		template := strings.Split(read.Path, "/")
		if len(template) != len(parts) {
			continue
		}
		matched := true
		for i, segment := range template {
			if strings.HasPrefix(segment, "{") {
				continue
			}
			decoded, err := url.PathUnescape(parts[i])
			matched = matched && err == nil && decoded == segment
		}
		if matched {
			return true
		}
	}
	return false
}
