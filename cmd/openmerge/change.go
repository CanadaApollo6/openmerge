package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/status"
)

func runChange(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || (args[0] != "brief" && args[0] != "show" && args[0] != "decisions") {
		_, _ = fmt.Fprintln(stderr, "Usage: openmerge change show|brief|decisions <id> --repo owner/name [--base main] [--audience reviewer|operator|agent] [--revision N] [--after N] [--limit N] [--json]")
		return 2
	}
	id := args[1]
	parseArgs := args[2:]
	if id == "--help" || id == "-h" {
		parseArgs = args[1:]
	}
	flags := flag.NewFlagSet("change "+args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	api := flags.String("api", envDefault("OPENMERGE_API_URL", "http://127.0.0.1:8081"), "status API origin")
	repo := flags.String("repo", "", "owner/name (required)")
	base := flags.String("base", "main", "base branch")
	audience := flags.String("audience", "reviewer", "reviewer, operator or agent")
	revision := flags.Uint64("revision", 0, "recorded revision (default latest)")
	after := flags.Int64("after", 0, "exclusive decision event cursor")
	limit := flags.Int("limit", 20, "decision page size (1-100)")
	asJSON := flags.Bool("json", false, "write public JSON response unchanged")
	tokenFile := flags.String("api-token-file", os.Getenv("OPENMERGE_API_TOKEN_FILE"), "read bearer token file")
	if err := flags.Parse(parseArgs); errors.Is(err, flag.ErrHelp) {
		return 0
	} else if err != nil {
		return 2
	}
	u, err := url.Parse(*api)
	parts := strings.Split(*repo, "/")
	afterSet, limitSet := false, false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "after":
			afterSet = true
		case "limit":
			limitSet = true
		}
	})
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		_, _ = fmt.Fprintln(stderr, "--api requires an http(s) origin without credentials, path, query or fragment")
		return 2
	}
	valid := flags.NArg() == 0 && queue.ChangeID(id).Validate() == nil && brief.ValidAudience(*audience) && len(parts) == 2 && parts[0] != "" && parts[1] != "" && parts[0] != "." && parts[0] != ".." && parts[1] != "." && parts[1] != ".." && !strings.ContainsAny(*repo, " \t\r\n?#%") && *base != "" && *base != "." && *base != ".." && *after >= 0 && *limit >= 1 && *limit <= 100
	if args[0] != "decisions" && (afterSet || limitSet) {
		valid = false
	}
	if args[0] == "show" && *audience != "reviewer" {
		_, _ = fmt.Fprintln(stderr, "show accepts only --audience reviewer")
		return 2
	}
	if !valid {
		_, _ = fmt.Fprintln(stderr, "change requires a canonical change ID, --repo owner/name, nonempty --base and valid options")
		return 2
	}
	token, err := status.ReadToken(os.Getenv("OPENMERGE_API_TOKEN"), *tokenFile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	path := "/v1/queues/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/" + url.PathEscape(*base) + "/changes/" + url.PathEscape(id)
	switch args[0] {
	case "brief":
		path += "/brief?audience=" + url.QueryEscape(*audience)
	case "decisions":
		path += "/decisions"
		query := make([]string, 0, 3)
		if limitSet {
			query = append(query, fmt.Sprintf("limit=%d", *limit))
		}
		if afterSet {
			query = append(query, fmt.Sprintf("after=%d", *after))
		}
		if *revision != 0 {
			query = append(query, fmt.Sprintf("revision=%d", *revision))
		}
		if len(query) > 0 {
			path += "?" + strings.Join(query, "&")
		}
	}
	if args[0] != "decisions" && *revision != 0 {
		separator := "?"
		if strings.Contains(path, "?") {
			separator = "&"
		}
		path += fmt.Sprintf("%srevision=%d", separator, *revision)
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(*api, "/")+path, nil)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "invalid API URL")
		return 2
	}
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "read status API:", err)
		return 1
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return queueAPIError(response, token, stderr)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, brief.MaxViewBytes+1))
	var view brief.View
	if args[0] == "show" {
		var result status.ChangeResponse
		if err != nil || len(raw) > brief.MaxViewBytes || json.Unmarshal(raw, &result) != nil || result.Version != 1 || result.PR == nil || result.PR.Change == nil || result.PR.Change.ID != id || result.Brief.ChangeID != id || result.Brief.Audience != "reviewer" {
			_, _ = fmt.Fprintln(stderr, "invalid, oversized or unsupported change API response")
			return 1
		}
		if !*asJSON {
			prefix := fmt.Sprintf("Change %s @%d: PR #%d %q\nAccountability: %s; gate: %q\n", id, result.PR.Change.Revision, result.PR.PR, result.PR.Status, result.PR.Change.Accountability.Status, result.PR.Gate)
			if landing := result.PR.Landing; landing != nil {
				prefix += fmt.Sprintf("Recorded landing: %q; commit %q; attempt %q; event %d\n", landing.Outcome, landing.SHA, landing.AttemptID, landing.EventID)
			}
			prefix += fmt.Sprintf("Decisions: %q\n", strings.TrimRight(*api, "/")+result.PR.Change.DecisionsURL)
			raw, err = brief.Render(result.Brief)
			raw = append([]byte(prefix), append(raw, '\n')...)
		}
		if err == nil {
			_, err = stdout.Write(raw)
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "write change:", err)
			return 1
		}
		return 0
	}
	if args[0] == "decisions" {
		var result status.DecisionsResponse
		if err != nil || len(raw) > brief.MaxViewBytes || json.Unmarshal(raw, &result) != nil || result.Version != status.Version || result.ChangeID != id || result.Decisions == nil || result.NextCursor < 0 {
			_, _ = fmt.Fprintln(stderr, "invalid, oversized or unsupported decisions API response")
			return 1
		}
		if *asJSON {
			_, err = stdout.Write(raw)
		} else {
			_, err = stdout.Write(renderDecisions(result))
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "write decisions:", err)
			return 1
		}
		return 0
	}
	if err != nil || len(raw) > brief.MaxViewBytes || json.Unmarshal(raw, &view) != nil || view.Version != 1 || view.Audience != *audience || view.ChangeID != id {
		_, _ = fmt.Fprintln(stderr, "invalid, oversized or unsupported brief API response")
		return 1
	}
	if !*asJSON {
		raw, err = brief.Render(view)
		raw = append(raw, '\n')
	}
	if err == nil {
		_, err = stdout.Write(raw)
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "write brief:", err)
		return 1
	}
	return 0
}

func renderDecisions(response status.DecisionsResponse) []byte {
	var out strings.Builder
	quote := func(value string) string {
		raw, _ := json.Marshal(value)
		return string(raw)
	}
	quoteList := func(values []string) string {
		raw, _ := json.Marshal(values)
		return string(raw)
	}
	writeSources := func(prefix string, sources []status.DecisionSourceView) {
		if sources == nil {
			fmt.Fprintf(&out, "%snot recorded\n", prefix)
			return
		}
		if len(sources) == 0 {
			fmt.Fprintf(&out, "%snone recorded\n", prefix)
			return
		}
		for _, source := range sources {
			fmt.Fprintf(&out, "%s%s via %s; source %s; read %s; receipt %s; intent %s; brief %s\n", prefix, quote(source.UserID), quote(source.Channel), quote(source.SourceID), quote(source.ReadID), quote(source.ReceiptID), quote(source.IntentDigest), quote(source.BriefDigest))
		}
	}

	fmt.Fprintf(&out, "Change %s decisions\n", quote(response.ChangeID))
	if len(response.Decisions) == 0 {
		fmt.Fprintln(&out, "  none recorded")
	}
	for _, decision := range response.Decisions {
		fmt.Fprintf(&out, "\nDecision event %d: batch %s; attempt %s; revision %d\n", decision.EventID, quote(decision.BatchID), quote(decision.AttemptID), decision.Revision)
		fmt.Fprintf(&out, "  Change: %s; variant: %s; outcome: %s\n", quote(decision.ChangeID), quote(decision.Variant), quote(decision.Outcome))
		fmt.Fprintf(&out, "  Eligibility event: %d; settlement event: %d; read: %s\n", decision.EligibilityEventID, decision.SettlementEventID, quote(decision.EligibilityReadID))
		fmt.Fprintf(&out, "  Tested SHA: %s; intended landing SHA: %s; landed SHA: %s\n", quote(decision.TestedSHA), quote(decision.IntendedLandingSHA), quote(decision.LandedSHA))
		fmt.Fprintf(&out, "  Digests: brief %s; intent %s; policy %s\n", quote(decision.BriefDigest), quote(decision.IntentDigest), quote(decision.PolicyDigest))
		if accountability := decision.Accountability; accountability != nil {
			fmt.Fprintf(&out, "  Accountability: %s\n", quote(accountability.Status))
			if accountability.Actor != nil {
				fmt.Fprintf(&out, "    Actor: %s (%s)\n", quote(accountability.Actor.ID), quote(accountability.Actor.Login))
			}
			if accountability.Author != nil {
				fmt.Fprintf(&out, "    Author: %s (%s)\n", quote(accountability.Author.ID), quote(accountability.Author.Login))
			}
			fmt.Fprintf(&out, "    Submitter: %s\n", quote(accountability.SubmitterID))
			for _, human := range accountability.Humans {
				fmt.Fprintf(&out, "    Accountable human: %s (%s)\n", quote(human.ID), quote(human.Login))
			}
		}
		fmt.Fprintln(&out, "  Sources:")
		writeSources("    ", decision.Sources)
		fmt.Fprintln(&out, "  Requirements:")
		if decision.Requirements == nil {
			fmt.Fprintln(&out, "    not recorded")
		} else if len(decision.Requirements) == 0 {
			fmt.Fprintln(&out, "    none recorded")
		} else {
			for _, requirement := range decision.Requirements {
				fmt.Fprintf(&out, "    %s at %s; side %s; selector %s; group %s; count %d; human %t; receipt %t\n", quote(requirement.Cause), quote(requirement.Path), quote(requirement.Side), quote(requirement.Selector), quote(requirement.Group), requirement.Count, requirement.Human, requirement.Receipt)
				fmt.Fprintf(&out, "      Pool IDs: %s; eligible IDs: %s\n", quoteList(requirement.PoolIDs), quoteList(requirement.EligibleIDs))
				fmt.Fprintln(&out, "      Relied on:")
				writeSources("        ", requirement.ReliedOn)
			}
		}
	}
	fmt.Fprintf(&out, "\nNext cursor: %d\n", response.NextCursor)
	return []byte(out.String())
}
