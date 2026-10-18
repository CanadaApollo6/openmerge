package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/brief"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/diagnostic"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

func briefHTTPView(t *testing.T, j store.Journal, id queue.ChangeID, sanitizer *diagnostic.Sanitizer) (brief.View, string) {
	t.Helper()
	path := "/v1/queues/" + j.Key.Repository + "/" + j.Key.BaseBranch + "/changes/" + string(id) + "/brief?audience=operator"
	w := httptest.NewRecorder()
	Handler(&source{journals: []store.Journal{j}}, []store.QueueKey{j.Key}, "", sanitizer).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("brief HTTP %d: %s", w.Code, w.Body)
	}
	var v brief.View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v, w.Body.String()
}

func briefRegressionBase(t *testing.T) (store.Journal, queue.ChangeID, queue.ApprovalBasis, queue.ChangeRevisionObserved) {
	t.Helper()
	k := key
	id, err := k.ChangeID(1)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	b := queue.ApprovalBasis{ChangeID: id, Revision: 1, Head: strings.Repeat("b", 40), ReadID: "brief-read-1", At: at}
	rev := queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 1, HeadSHA: b.Head, Basis: queue.RevisionObservationBasis{BaseSHA: strings.Repeat("a", 40), At: at.Add(-time.Hour)}}
	j := store.Journal{Key: k, Seed: queue.State{BaseBranch: k.BaseBranch}}
	j.Events = append(j.Events,
		store.JournalEvent{Event: rev},
		store.JournalEvent{Event: queue.Intent(b, "recorded title", "recorded body")},
		store.JournalEvent{Event: queue.PRObserved{PR: 1, HeadSHA: b.Head, Eligible: true, Trusted: true}},
	)
	return j, id, b, rev
}

func briefRegressionStance(b queue.ApprovalBasis, kind cp.StanceKind, seq uint64, user string, at time.Time) queue.ReviewStanceObserved {
	if user == "" {
		user = "github.com:user:2"
	}
	b.ReadID = "brief-read-" + strconv.FormatUint(seq, 10)
	b.At = at
	source := cp.StanceSource{Channel: cp.CommentCommand, ID: strconv.FormatUint(seq, 10), Sequence: seq, Time: at, TimeKnown: true, UpperTimeBound: at.Add(time.Second), Precision: cp.ExactTime, ActingUserID: user}
	stance := cp.Stance{UserID: user, Kind: kind, Revision: uint64(b.Revision), Head: b.Head, Source: source}
	if kind == cp.Approve {
		stance.Receipt = &cp.ReceiptIdentity{ChangeID: string(b.ChangeID), Revision: uint64(b.Revision), Generation: 1, Base: strings.Repeat("a", 40), Head: b.Head, MergeBase: strings.Repeat("c", 40), IntentDigest: "intent-digest", PolicyDigest: "policy-digest", BriefDigest: "brief-digest"}
	}
	return queue.ReviewStanceObserved{Basis: b, Reviewer: cp.Identity{ID: user, Login: "reviewer", Kind: cp.HumanAccount}, Stance: stance, SourceKind: forge.CommentCreationTime, SourceTime: forge.NativeTime{Time: at, Source: forge.CommentCreationTime, Precision: time.Second, UpperBound: at.Add(time.Second)}, Observation: forge.NativeObservation{ObservedAt: at, ResponseDate: at}}
}

func briefAppend(j *store.Journal, event queue.Event) {
	j.Events = append(j.Events, store.JournalEvent{Event: event})
}

func TestBriefHTTPRegisteredSanitizerCoversSecretsInQueueEvidence(t *testing.T) {
	apiToken := "api-token-brief-regression"
	webhook := "webhook-secret-brief-regression"
	dsn := "postgres://brief_user:dsn-password-brief@db.internal/openmerge"
	password := "dsn-password-brief"
	j := journal(
		queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true},
		queue.BlockSourceBlocked{Source: queue.ConfigSource, Detail: "api=" + apiToken + " webhook=" + webhook + " dsn=" + dsn},
	)
	id, _ := j.Key.ChangeID(1)
	_, body := briefHTTPView(t, j, id, diagnostic.NewSanitizer(apiToken, webhook, dsn))
	for _, secret := range []string{apiToken, webhook, dsn, password} {
		if strings.Contains(body, secret) {
			t.Fatalf("registered secret leaked in brief: %q", secret)
		}
	}
}

func TestBriefHTTPStancesReceiptsKnownEmptyAndNewRevision(t *testing.T) {
	for _, kind := range []cp.StanceKind{cp.RequestChanges, cp.Withdraw} {
		j, id, basis, _ := briefRegressionBase(t)
		stance := briefRegressionStance(basis, kind, 10, "", basis.At.Add(-time.Minute))
		if err := stance.Validate(); err != nil {
			t.Fatal(err)
		}
		briefAppend(&j, stance)
		v, _ := briefHTTPView(t, j, id, nil)
		if len(v.Stances) != 1 || v.Stances[0].Kind != string(kind) {
			t.Fatalf("missing %s stance: %+v", kind, v.Stances)
		}
		if v.Stances[0].NextStep.Action != "reobserve_then_fresh_approval" {
			t.Fatalf("stance recovery lost source-time bound: %+v", v.Stances[0].NextStep)
		}
	}

	j, id, basis, _ := briefRegressionBase(t)
	briefAppend(&j, briefRegressionStance(basis, cp.Approve, 11, "", basis.At))
	v, _ := briefHTTPView(t, j, id, nil)
	if len(v.Stances) != 1 || v.Stances[0].Kind != string(cp.Approve) || v.Stances[0].IntentDigest == brief.NotRecorded || v.Stances[0].BriefDigest == brief.NotRecorded {
		t.Fatalf("receipt binding not projected: %+v", v.Stances)
	}

	j, id, basis, rev := briefRegressionBase(t)
	// Approval facts exist but contain no sources: the public arrays must be
	// known-empty, not null, after evidence has been recorded.
	v, _ = briefHTTPView(t, j, id, nil)
	if v.Stances == nil || v.Tests == nil {
		t.Fatalf("known-empty evidence became null: stances=%v tests=%v", v.Stances == nil, v.Tests == nil)
	}
	briefAppend(&j, briefRegressionStance(basis, cp.Approve, 42, "", basis.At))
	newHead := strings.Repeat("d", 40)
	briefAppend(&j, queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 2, Predecessor: rev.Revision, HeadSHA: newHead, Basis: queue.RevisionObservationBasis{BaseSHA: rev.Basis.BaseSHA, At: rev.Basis.At.Add(time.Hour)}})
	briefAppend(&j, queue.PRObserved{PR: 1, HeadSHA: newHead, Eligible: true, Trusted: true})
	v, _ = briefHTTPView(t, j, id, nil)
	for _, stance := range v.Stances {
		if stance.Revision == uint64(basis.Revision) {
			t.Fatalf("stale revision stance projected as current: %+v", v.Stances)
		}
	}
}

func TestBriefHTTPRevisionZeroSourceRemainsVisibleAsAmbiguous(t *testing.T) {
	j, id, basis, rev := briefRegressionBase(t)
	secondHead := strings.Repeat("d", 40)
	briefAppend(&j, queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 2, Predecessor: 1, HeadSHA: secondHead, Basis: queue.RevisionObservationBasis{BaseSHA: rev.Basis.BaseSHA, At: rev.Basis.At.Add(time.Hour)}})
	briefAppend(&j, queue.ChangeRevisionObserved{PR: 1, ChangeID: id, Revision: 3, Predecessor: 2, HeadSHA: basis.Head, Basis: queue.RevisionObservationBasis{BaseSHA: rev.Basis.BaseSHA, At: rev.Basis.At.Add(2 * time.Hour)}})
	lateDismissal := queue.ReviewStanceObserved{
		Basis:              queue.ApprovalBasis{ChangeID: id, Revision: 3, Head: basis.Head, ReadID: "late-dismissal", At: rev.Basis.At.Add(3 * time.Hour)},
		Reviewer:           cp.Identity{ID: "github.com:user:2", Login: "reviewer", Kind: cp.HumanAccount},
		Stance:             cp.Stance{UserID: "github.com:user:2", Kind: cp.Withdraw, Revision: 3, Head: basis.Head, Source: cp.StanceSource{Channel: cp.NativeReview, ID: "10", Sequence: 10, Time: basis.At, TimeKnown: false, UpperTimeBound: basis.At.Add(3 * time.Second), Precision: cp.ObservationFallback, DismissedReviewID: "10", ActingUserID: "github.com:user:9"}},
		SourceKind:         forge.ObservationTimeFallback,
		SourceTime:         forge.NativeTime{Time: basis.At, Source: forge.ObservationTimeFallback, Precision: time.Second, Uncertainty: 2 * time.Second, UpperBound: basis.At.Add(3 * time.Second)},
		OriginalSubmission: forge.NativeTime{Time: basis.At.Add(-time.Hour), Source: forge.ReviewSubmissionTime},
	}
	if err := lateDismissal.Validate(); err != nil {
		t.Fatal(err)
	}
	briefAppend(&j, lateDismissal)
	v, _ := briefHTTPView(t, j, id, nil)
	foundAmbiguous := false
	for _, stance := range v.Stances {
		if stance.Revision == 0 {
			if stance.Attribution != "not counted: ambiguous attribution" {
				t.Fatal("ambiguous stance presented as counted")
			}
			foundAmbiguous = true
		}
	}
	if !foundAmbiguous {
		t.Fatalf("revision-zero source was hidden: %+v", v.Stances)
	}
	for _, warning := range v.Warnings {
		if warning.Code == "ambiguous_attribution" {
			return
		}
	}
	t.Fatal("ambiguous source warning missing")
}

func TestBriefHTTPSaturationAndEscapingTruncation(t *testing.T) {
	j, id, basis, _ := briefRegressionBase(t)
	for i := 0; i <= 4096; i++ {
		user := "github.com:user:" + strconv.Itoa(100+i)
		briefAppend(&j, briefRegressionStance(basis, cp.Approve, uint64(10000+i), user, basis.At.Add(time.Duration(i)*time.Second)))
	}
	v, body := briefHTTPView(t, j, id, nil)
	if !strings.Contains(body, "approval_saturated") {
		t.Fatal("saturation warning missing")
	}
	if v.Stances == nil || len(v.Stances) != 0 {
		t.Fatal("saturated evidence lost known-empty stance section")
	}

	j, id, basis, _ = briefRegressionBase(t)
	briefAppend(&j, queue.Intent(basis, "<script>title</script>", strings.Repeat("<script>&long-body</script>", 300)))
	v, body = briefHTTPView(t, j, id, nil)
	if !v.Truncated || strings.Contains(body, "<script>") {
		t.Fatalf("brief escaping/truncation regressed: truncated=%v", v.Truncated)
	}
}

func TestBriefHTTPRevisionZeroFixtureRemainsWireCompatible(t *testing.T) {
	data, err := os.ReadFile("../queue/testdata/approvals/dismissal.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.DecodeEvent(data); err != nil {
		t.Fatalf("approval fixture no longer decodes: %v", err)
	}
}

func TestBriefHTTPEveryQueueBlockHasCorrectRecovery(t *testing.T) {
	j, id, _, _ := briefRegressionBase(t)
	sources := []queue.BlockSource{queue.RepositoryAccess, queue.ConfigSource, queue.CheckSources, queue.LandingNotOnBase, queue.ActionPoison, queue.Operator, queue.BuildFailed}
	for _, source := range sources {
		e := queue.BlockSourceBlocked{Source: source, Detail: "blocked"}
		if source == queue.LandingNotOnBase || source == queue.ActionPoison {
			e.Subject = "recorded-subject"
		}
		briefAppend(&j, e)
	}
	v, _ := briefHTTPView(t, j, id, nil)
	seen := map[string]bool{}
	for _, w := range v.Warnings {
		if w.Code != "queue_blocked" {
			continue
		}
		source := queue.BlockSource(w.NextStep.Source)
		expected := "wait_for_recovery"
		if source.OperatorClearable() {
			expected = "refresh"
		}
		if w.NextStep.Action != expected || !strings.Contains(w.Clears, "@openmerge refresh") {
			t.Fatalf("wrong recovery: %+v", w)
		}
		seen[string(source)] = true
	}
	for _, source := range sources {
		if !seen[string(source)] {
			t.Fatalf("missing block %s", source)
		}
	}
}

func TestBriefHTTPExactHeadTrustAndRebaseRecovery(t *testing.T) {
	for _, rebase := range []bool{false, true} {
		events := []queue.Event{queue.BaseMoved{SHA: "recorded-base"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: rebase}, queue.EnqueueRequested{PR: 1, At: at}}
		action, commit := "trust_exact_head", "head"
		if rebase {
			events = append(events, queue.Dequeued{PR: 1, AdmissionID: 1, Reason: queue.Conflict})
			action, commit = "rebase_onto", "recorded-base"
		}
		j := journal(events...)
		id, _ := j.Key.ChangeID(1)
		v, _ := briefHTTPView(t, j, id, nil)
		found := false
		for _, w := range v.Warnings {
			if w.NextStep.Action == action {
				found = true
				if w.NextStep.Commit != commit || w.Clears == "" {
					t.Fatalf("unbound recovery %+v", w)
				}
			}
		}
		if !found {
			t.Fatalf("missing %s in %+v", action, v.Warnings)
		}
	}
}

func TestBriefHTTPFinalDisplaySanitizesIntentHunksAndPaths(t *testing.T) {
	const secret = "opaque-installation-secret-987654"
	for _, field := range []string{"intent", "hunk", "path"} {
		t.Run(field, func(t *testing.T) {
			j, data, _, b := policyJournal(t, true)
			if field == "intent" {
				briefAppend(&j, queue.Intent(b, secret, secret))
			}
			for i, row := range j.Events {
				e, ok := row.Event.(queue.ChangeClassified)
				if !ok {
					continue
				}
				c := &e.Evidence.Classification
				if field == "hunk" {
					c.Files[0].Hunks.Hunks[0].Text = "+" + secret + "\n"
				}
				if field == "path" {
					c.Files[0].NewPath = secret
					c.Paths = []string{secret}
				}
				if err := e.Validate(); err != nil {
					t.Fatal(err)
				}
				j.Events[i].Event = e
			}
			before, _ := json.Marshal(j)
			s := &historicalPolicySource{source: source{journals: []store.Journal{j}}, data: data}
			for _, audience := range []string{"agent", "operator", "reviewer"} {
				request := func(sanitizer *diagnostic.Sanitizer) brief.View {
					w := httptest.NewRecorder()
					Handler(s, []store.QueueKey{j.Key}, "", sanitizer).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/Owner/Repo/release/changes/"+string(b.ChangeID)+"/brief?audience="+audience, nil))
					var v brief.View
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
						t.Fatalf("HTTP %d %s", w.Code, w.Body)
					}
					return v
				}
				original := request(nil)
				v := request(diagnostic.NewSanitizer(secret))
				raw, _ := json.Marshal(v)
				if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "[REDACTED]") || !strings.Contains(string(raw), "display_redacted") {
					t.Fatalf("bad redaction: %s", raw)
				}
				if v.Digest != original.Digest {
					t.Fatal("display redaction changed digest")
				}
			}
			after, _ := json.Marshal(j)
			if string(before) != string(after) {
				t.Fatal("display mutated recorded facts")
			}
		})
	}
}

func TestBriefDisplaySanitizationPreservesRecordedDigest(t *testing.T) {
	raw, err := os.ReadFile("../queue/testdata/brief-facts.json")
	if err != nil {
		t.Fatal(err)
	}
	var f brief.Fact
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	const secret = "opaque-installation-secret-987654"
	f.Intent.Title, f.Intent.Body = secret, secret
	f.Intent.Digest = cp.IntentDigest(secret, secret)
	f.Classification.Files[0].Hunks.Hunks[0].Text = secret
	f.Classification.Files[0].NewPath = secret
	before, err := brief.Digest(f)
	if err != nil {
		t.Fatal(err)
	}
	v, err := brief.Project(f, "agent", diagnostic.NewSanitizer(secret))
	if err != nil {
		t.Fatal(err)
	}
	if v.Digest != before || !strings.Contains(mustBriefJSON(t, v), "display_redacted") {
		t.Fatal("missing original digest or sanitization")
	}
	after, err := brief.Digest(f)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || v.Digest != before {
		t.Fatal("display changed recorded digest")
	}
	display, _ := json.Marshal(v)
	if strings.Contains(string(display), secret) {
		t.Fatal("secret leaked")
	}
}

func mustBriefJSON(t *testing.T, v brief.View) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
