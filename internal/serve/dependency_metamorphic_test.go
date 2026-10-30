package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/dependency"
	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

type capturedReport struct {
	Head   string
	Status forge.Status
}

type privacyReportDriver struct {
	*instructionDriver
	payloads map[int][]capturedReport
}

func (d *privacyReportDriver) ReportStatus(ctx context.Context, repo forge.RepoRef, pr int, head string, status forge.Status) error {
	d.payloads[pr] = append(d.payloads[pr], capturedReport{Head: head, Status: status})
	return d.instructionDriver.ReportStatus(ctx, repo, pr, head, status)
}

// Exercise the real report and durable deduplication, including the interval
// after observation but before the planner executes a pending dequeue.
func TestDependencyPendingPublicationPrivacy(t *testing.T) {
	for _, transition := range []string{"head", "closed", "requested", "retargeted", "paused"} {
		t.Run(transition, func(t *testing.T) {
			var baseline []byte
			baselineWrites := -1
			for _, phase := range []string{"queued", "testing"} {
				t.Run(phase, func(t *testing.T) {
					db, conn := databaseConnection(t)
					s, w, f, _ := fixtureWithStore(t, repositoryConfig, db)
					code := "dependency-waiting"
					if phase == "testing" {
						code = "dependency-landed"
					}
					j, b, _, _, _ := statusfixture.MultiJournal(t, code, phase, 1, true)
					id, err := db.EnsureQueue(t.Context(), j.Key)
					must(t, err)
					w.id = id
					w.Key = j.Key
					r := repo
					r.Path = j.Key.Repository
					f.Init(r, "main", "fence")
					name, raw, err := queue.EncodeEvent(queue.BaseMoved{SHA: j.Seed.BaseSHA})
					must(t, err)
					_, err = conn.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload) VALUES(100,$1,$2,$3::jsonb)`, id, name, string(raw))
					must(t, err)
					for _, e := range j.Events {
						var data any
						must(t, json.Unmarshal(e.Payload, &data))
						shiftDependencyFixtureRefs(data)
						raw, err = json.Marshal(data)
						must(t, err)
						var envelope struct {
							Type string `json:"type"`
						}
						must(t, json.Unmarshal(raw, &envelope))
						_, err = conn.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`, e.ID+100, id, envelope.Type, string(raw))
						must(t, err)
					}
					next := int64(len(j.Events) + 101)
					insert := func(event queue.Event) {
						t.Helper()
						name, raw, err := queue.EncodeEvent(event)
						must(t, err)
						_, err = conn.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`, next, id, name, string(raw))
						must(t, err)
						next++
					}
					if phase == "testing" {
						insert(queue.CheckProvenanceObserved{BatchID: "b1", TestedSHA: fmt.Sprintf("%040x", 100), Checks: []queue.UnboundCheck{{Name: "ci", Source: "github-actions", HeadBranch: "other"}}})
					}
					switch transition {
					case "head":
						insert(queue.PRObserved{PR: 3, HeadSHA: strings.Repeat("9", 40), Eligible: true, Trusted: true})
					case "closed":
						insert(queue.PRClosed{PR: 3})
					case "requested":
						insert(queue.DequeueRequested{PR: 3})
					case "retargeted":
						insert(queue.PRObserved{PR: 3, HeadSHA: fmt.Sprintf("%040x", 13), Eligible: false, Trusted: true})
					case "paused":
						insert(queue.PauseRequested{Reason: "operator", Detail: "public maintenance"})
					}
					w.dependencies = &dependencyPass{binding: b, sources: statusfixture.StatusSources()}
					w.prs = map[int]forge.PullRequest{}
					state, _, err := db.Load(t.Context(), id)
					must(t, err)
					for _, e := range state.Entries {
						w.prs[e.PR] = forge.PullRequest{Number: e.PR, HeadSHA: e.HeadSHA}
					}
					driver := &instructionDriver{Driver: w.Driver}
					w.Driver = driver
					must(t, s.report(t.Context(), w))
					got, err := db.Observation(t.Context(), id, "status/3")
					must(t, err)
					if len(got) == 0 {
						t.Fatal("no actual publication")
					}
					if baseline == nil {
						baseline = bytes.Clone(got)
						baselineWrites = driver.writes
					} else if !bytes.Equal(baseline, got) || baselineWrites != driver.writes {
						t.Fatalf("publication differs: writes %d/%d\n%s\n%s", baselineWrites, driver.writes, baseline, got)
					}
					writes := driver.writes
					must(t, s.report(t.Context(), w))
					if driver.writes != writes {
						t.Fatalf("unchanged report published %d extra checks", driver.writes-writes)
					}
				})
			}
		})
	}
}

func shiftDependencyFixtureRefs(value any) {
	shiftFixtureRefs(value, func(n float64) float64 { return n + 100 })
}

func shiftFixtureRefs(value any, shift func(float64) float64) {
	switch v := value.(type) {
	case map[string]any:
		for k, x := range v {
			if k == "observation_ids" || k == "composition_ids" || k == "cause_event_ids" {
				for i, n := range x.([]any) {
					x.([]any)[i] = shift(n.(float64))
				}
				continue
			}
			if n, ok := x.(float64); ok && n > 0 && (strings.HasSuffix(k, "event_id") || k == "expected_prior") {
				v[k] = shift(n)
			} else {
				shiftFixtureRefs(x, shift)
			}
		}
	case []any:
		for _, x := range v {
			shiftFixtureRefs(x, shift)
		}
	}
}

// metamorphicServeQueue loads a world into a fresh queue of the shared schema.
// The private tail is inserted after any authority pin so it advances the DB
// watermark exactly as a later private read would.
type metamorphicServeQueue struct {
	s      *Server
	w      *worker
	driver *privacyReportDriver
	conn   *pgx.Conn
	tail   func()
}

func newMetamorphicServeQueue(t *testing.T, db *store.Store, conn *pgx.Conn, m statusfixture.MetamorphicWorld, pin []byte) metamorphicServeQueue {
	t.Helper()
	for _, table := range []string{"events", "observations", "actions", "queues"} {
		_, err := conn.Exec(t.Context(), "TRUNCATE "+table+" CASCADE")
		must(t, err)
	}
	s, w, f, _ := fixtureWithStore(t, repositoryConfig, db)
	j := m.Journal
	id, err := db.EnsureQueue(t.Context(), j.Key)
	must(t, err)
	w.id = id
	w.Key = j.Key
	r := repo
	r.Path = j.Key.Repository
	f.Init(r, "main", "fence")
	insert := func(eventID int64, raw []byte, shift func(float64) float64) {
		t.Helper()
		var data any
		must(t, json.Unmarshal(raw, &data))
		shiftFixtureRefs(data, shift)
		raw, err := json.Marshal(data)
		must(t, err)
		var envelope struct {
			Type string `json:"type"`
		}
		must(t, json.Unmarshal(raw, &envelope))
		_, err = conn.Exec(t.Context(), `INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`, eventID, id, envelope.Type, string(raw))
		must(t, err)
	}
	identity := func(n float64) float64 { return n }
	if pin != nil {
		// The pin binds the effective policy digest recorded before the journal.
		config, err := os.ReadFile("../queue/testdata/wire/v1/event_config_applied.json")
		must(t, err)
		insert(99, config, identity)
	}
	_, raw, err := queue.EncodeEvent(queue.BaseMoved{SHA: j.Seed.BaseSHA})
	must(t, err)
	insert(100, raw, identity)
	shift := func(n float64) float64 { return n + 100 }
	for _, e := range j.Events {
		insert(e.ID+100, e.Payload, shift)
	}
	delta := int64(100)
	if pin != nil {
		event, err := queue.DecodeEvent(pin)
		must(t, err)
		applied := event.(queue.PolicyAuthorityApplied)
		applied.Authority, applied.OpenMerge = authority.GitHub, nil
		_, raw, err := queue.EncodeEvent(applied)
		must(t, err)
		insert(int64(len(j.Events))+101, raw, identity)
		delta++
	}
	boundary := float64(len(j.Events))
	tail := func() {
		for _, e := range m.Tail {
			insert(e.ID+delta, e.Payload, func(n float64) float64 {
				if n > boundary {
					return n + float64(delta)
				}
				return n + 100
			})
		}
	}
	w.dependencies = &dependencyPass{binding: m.Binding, sources: statusfixture.StatusSources()}
	w.prs = map[int]forge.PullRequest{}
	state, _, err := db.Load(t.Context(), id)
	must(t, err)
	if pin != nil && state.AuthorityPin() == nil {
		t.Fatal("authority pin was not applied")
	}
	for _, e := range state.Entries {
		w.prs[e.PR] = forge.PullRequest{Number: e.PR, HeadSHA: e.HeadSHA}
	}
	heads := map[int]string{}
	for _, e := range j.Events {
		if observed, ok := e.Event.(queue.PRObserved); ok {
			heads[observed.PR] = observed.HeadSHA
		}
	}
	for _, pr := range m.PRs {
		if _, ok := w.prs[pr]; !ok {
			// A departed PR keeps the check published while it was queued; an
			// authority pin refreshes that retained check.
			w.prs[pr] = forge.PullRequest{Number: pr, HeadSHA: heads[pr]}
			must(t, s.writeStatus(t.Context(), w, pr, heads[pr], forge.Status{State: forge.Queued, Summary: "Waiting for a test batch"}))
		}
	}
	driver := &privacyReportDriver{instructionDriver: &instructionDriver{Driver: w.Driver}, payloads: map[int][]capturedReport{}}
	w.Driver = driver
	return metamorphicServeQueue{s: s, w: w, driver: driver, conn: conn, tail: tail}
}

func (q metamorphicServeQueue) durable(t *testing.T, prs []int) map[int][]byte {
	t.Helper()
	got := map[int][]byte{}
	for _, pr := range prs {
		raw, err := q.s.store.Observation(t.Context(), q.w.id, fmt.Sprintf("status/%d", pr))
		must(t, err)
		got[pr] = raw
	}
	return got
}

func (q metamorphicServeQueue) report(t *testing.T) int {
	t.Helper()
	writes := q.driver.writes
	must(t, q.s.report(t.Context(), q.w))
	return q.driver.writes - writes
}

// Compare actual forge arguments, durable records and publication counts across
// private worlds; then revoke an authorized check cache at an unchanged journal
// watermark and require the fresh unauthorized publication.
func TestDependencyMetamorphicPublication(t *testing.T) {
	pin, err := os.ReadFile("../queue/testdata/wire/v1/event_policy_authority_applied.json")
	must(t, err)
	cases := statusfixture.MetamorphicCases(testing.Short())
	if testing.Short() {
		cases = cases[:5]
	}
	for i, c := range cases {
		pinned := i%2 == 0
		t.Run(fmt.Sprintf("%s/github-pin-%t", c, pinned), func(t *testing.T) {
			db, conn := databaseConnection(t)
			type outcome struct {
				writes  int
				payload []byte
				durable map[int][]byte
			}
			baseline := map[string]outcome{}
			var worlds []statusfixture.MetamorphicWorld
			exposed := false
			for _, world := range statusfixture.MetamorphicWorlds(testing.Short()) {
				m := statusfixture.Metamorphic(t, c, world)
				worlds = append(worlds, m)
				var pinEvent []byte
				if pinned {
					pinEvent = pin
				}
				q := newMetamorphicServeQueue(t, db, conn, m, pinEvent)
				writes := q.report(t)
				if writes == 0 {
					t.Fatal("report published no checks")
				}
				payload, err := json.Marshal(q.driver.payloads)
				must(t, err)
				got := outcome{writes, payload, q.durable(t, m.PRs)}
				switch want, ok := baseline[m.Group]; {
				case !m.Compared():
					// Rendered and checked below, never a comparison partner.
				case !ok:
					baseline[m.Group] = got
				default:
					if want.writes != got.writes {
						t.Fatalf("world %d wrote %d checks, want %d", world, got.writes, want.writes)
					}
					if !bytes.Equal(want.payload, got.payload) {
						t.Fatalf("world %d sent different forge payloads %s", world, statusfixture.Diff(want.payload, got.payload))
					}
					for _, pr := range m.PRs {
						if !bytes.Equal(want.durable[pr], got.durable[pr]) {
							t.Fatalf("world %d PR %d publication differs %s", world, pr, statusfixture.Diff(want.durable[pr], got.durable[pr]))
						}
					}
				}
				if n := q.report(t); n != 0 {
					t.Fatalf("world %d republished %d unchanged checks", world, n)
				}
				q.tail()
				if n := q.report(t); n != 0 {
					t.Fatalf("world %d published %d checks for a private-only read", world, n)
				}
				for pr, raw := range q.durable(t, m.PRs) {
					if !bytes.Equal(raw, got.durable[pr]) {
						t.Fatalf("world %d PR %d changed after a private-only read %s", world, pr, statusfixture.Diff(got.durable[pr], raw))
					}
				}

				q = newMetamorphicServeQueue(t, db, conn, m, pinEvent)
				q.w.dependencies.checkAudience = map[string][]dependency.Grant{m.Source: {m.Grant}}
				q.report(t)
				authorized := q.durable(t, m.PRs)
				q.w.dependencies.checkAudience = nil
				q.report(t)
				for pr, raw := range q.durable(t, m.PRs) {
					exposed = exposed || !bytes.Equal(authorized[pr], got.durable[pr])
					if !bytes.Equal(raw, got.durable[pr]) {
						t.Fatalf("world %d PR %d kept authorized output after revocation %s", world, pr, statusfixture.Diff(got.durable[pr], raw))
					}
				}
			}
			if !exposed {
				t.Fatal("authorized audience never changed a publication; revocation is untested")
			}
			statusfixture.RequireComparedWorlds(t, c, worlds)
		})
	}
}
