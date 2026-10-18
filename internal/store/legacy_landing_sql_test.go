package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/executor"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type landingSQLTrace struct {
	mu         sync.Mutex
	statements []string
}

func (l *landingSQLTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statements = append(l.statements, strings.Join(strings.Fields(data.SQL), " "))
	return ctx
}
func (*landingSQLTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func legacyExecutorInput(t *testing.T, s *Store) (QueueID, ClaimedAction, landingCase, executor.Executor) {
	t.Helper()
	f := fake.New()
	repo := forge.RepoRef{Path: "Owner/Repo"}
	base, fence := f.Init(repo, "release", "openmerge/fence/release")
	pr, err := f.OpenPR(repo, 12, base, true)
	must(t, err)
	tested, err := f.BuildTestRef(t.Context(), repo, forge.BuildRequest{Ref: "openmerge/queue/release/b1", BaseSHA: base, PRs: []forge.BuildPR{{Number: 12, HeadSHA: pr.HeadSHA}}, Method: forge.Squash, BatchID: "b1"})
	must(t, err)
	raw, err := os.ReadFile("../queue/testdata/landing-start.json")
	must(t, err)
	for old, next := range map[string]string{strings.Repeat("a", 40): base, strings.Repeat("b", 40): pr.HeadSHA, strings.Repeat("e", 40): tested, `"fence"`: `"` + fence + `"`} {
		raw = bytes.ReplaceAll(raw, []byte(old), []byte(next))
	}
	q, claim, c, _ := decisionLandInput(t, s, raw)
	f.SetCheckRuns(repo, tested, []forge.CheckRun{{Name: "ci", State: forge.Success}})
	x := executor.Executor{Driver: f, Git: f, Repo: repo, Base: "release", RequiredChecks: []string{"ci"}, Now: func() time.Time { return c.Start.At }, NewID: func() (string, error) { return c.Start.AttemptID, nil }}
	return q, claim, c, x
}

// The identical harness is also overlaid on origin/main by the SQL differential
// script. It runs the production executor -> Store.Emit path, not StartLanding.
func TestLegacyExecutorEmitSQL(t *testing.T) {
	s := newStore(t)
	q, claim, c, x := legacyExecutorInput(t, s)
	original := s.pool
	trace := &landingSQLTrace{}
	cfg := original.Config().Copy()
	cfg.ConnConfig.Tracer = trace
	traced, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	s.pool = traced
	defer original.Close()
	stop := errors.New("stop after start")
	err = x.Execute(t.Context(), c.Land, func(e queue.Event) error {
		if _, ok := e.(queue.LandingCleanupPolicyRecorded); ok {
			return stop
		}
		return s.Emit(t.Context(), claim, c.Config)(e)
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	// Capture before inspection adds SQL; this includes BEGIN, advisory lock,
	// claim lock, journal load, event insert and COMMIT in order.
	raw, err := json.Marshal(trace.statements)
	must(t, err)
	t.Logf("LEGACY-LANDING-SQL %s", raw)
	state, _, err := s.Load(t.Context(), q)
	must(t, err)
	if state.Active == nil || state.Active.Phase != queue.Landing || len(state.LandingDecisions()) != 0 {
		t.Fatal("legacy start changed")
	}
	records, err := s.EventsAfter(t.Context(), q, 0)
	must(t, err)
	if !reflect.DeepEqual(records[len(records)-1].Event, c.Start) {
		t.Fatal("LandingStarted changed")
	}
}
