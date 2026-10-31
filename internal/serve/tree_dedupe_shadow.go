package serve

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/CanadaApollo6/openmerge/internal/treededupe"
)

// TreeDedupeShadow enables ADR 0015 slice 1: after an action's events are
// durably committed, record what exact-tree dedupe WOULD have reused. It is
// an internal seam with no CLI or configuration surface, and nil (the
// production default) leaves every code path byte-for-byte unchanged. When
// set, it still never changes a decision: it reads committed events, writes
// only the store's shadow side row after the action's claim is completed,
// and swallows every failure.
type TreeDedupeShadow struct {
	// Inputs reads the tested commit's key inputs with Git's own readers. Nil
	// uses the target's Git when it implements TreeDedupeInputs (gitops does);
	// otherwise every key is unknown.
	Inputs func(context.Context, store.QueueKey, string) (treededupe.GitInputs, error)
	// RunnerImage returns an immutable runner image identity for the queue,
	// or "" when unknown. An unknown image makes every key unknown, so no
	// shadow hit is possible until an activation slice supplies one.
	RunnerImage func(store.QueueKey) string
	// Now stamps outcome observations; nil uses time.Now.
	Now func() time.Time
	// Timeout bounds all shadow I/O for one action; zero selects 2s.
	Timeout time.Duration
}

type treeDedupeInputsReader interface {
	TreeDedupeInputs(context.Context, forge.RepoRef, string) (treededupe.GitInputs, error)
}

// treeDedupeCapture collects one action's committed events and the facts the
// shadow needs, captured before execution so later config edits cannot leak in.
type treeDedupeCapture struct {
	state  queue.State
	retry  bool
	checks string
	events []queue.Event
}

func (s *Server) treeDedupeCapture(w *worker, state queue.State, cfg queue.Config) *treeDedupeCapture {
	if s.options.TreeDedupeShadow == nil {
		return nil
	}
	c := &treeDedupeCapture{state: state, retry: cfg.RetryLimit() > 0}
	if w.configError == nil {
		c.checks = "merge_when=" + w.cfg.MergeWhen.String()
		if w.stageA != nil {
			c.checks += "\x00stage_a_policy=" + w.stageA.policy.Digest
		}
	}
	return c
}

func (c *treeDedupeCapture) add(event queue.Event) {
	if c != nil {
		c.events = append(c.events, event)
	}
}

// recordTreeDedupeShadow folds committed events into the shadow ledger. It
// runs after the claim completes and must never fail, block or panic into
// the worker: errors and panics are logged and dropped.
func (s *Server) recordTreeDedupeShadow(w *worker, c *treeDedupeCapture) {
	opts := s.options.TreeDedupeShadow
	if opts == nil || c == nil || len(c.events) == 0 {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.options.Logger.Warn("tree dedupe shadow dropped", "queue", w.Key.Repository, "base", w.Key.BaseBranch, "panic", fmt.Sprint(r))
		}
	}()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	// Fold committed events onto the pre-action state only to label builds;
	// the result is discarded and never reaches the planner.
	after := c.state
	var apply []func(*treededupe.Ledger)
	for _, event := range c.events {
		after = queue.Apply(after, event)
		switch e := event.(type) {
		case queue.TestRefBuilt:
			apply = append(apply, s.treeDedupeBuild(ctx, w, c, after, e.BatchID, e.TestedSHA, e.At, treededupe.KindBatch))
		case queue.ChainedTestRefBuilt:
			apply = append(apply, s.treeDedupeBuild(ctx, w, c, after, e.BatchID, e.TestedSHA, e.At, treededupe.KindChained))
		case queue.FastForwardRefBuilt:
			apply = append(apply, s.treeDedupeBuild(ctx, w, c, after, e.BatchID, e.TestedSHA, e.At, treededupe.KindFastForward))
		case queue.ChecksCompleted:
			at := now()
			apply = append(apply, func(l *treededupe.Ledger) { l.ObserveOutcome(e.TestedSHA, at, e.Success) })
		case queue.ChecksObserved:
			at := now()
			apply = append(apply, func(l *treededupe.Ledger) {
				if e.Terminal {
					l.ObserveOutcome(e.TestedSHA, at, e.Success)
				} else {
					l.ObserveReopened(e.TestedSHA)
				}
			})
		case queue.ChecksReopened:
			apply = append(apply, func(l *treededupe.Ledger) { l.ObserveReopened(e.TestedSHA) })
		case queue.ChecksFailureConfirmed:
			if e.Reason == queue.ChecksFailed {
				at := now()
				apply = append(apply, func(l *treededupe.Ledger) { l.ObserveConfirmedFailure(e.BatchID, e.TestedSHA, at) })
			}
		}
	}
	if len(apply) == 0 {
		return
	}
	err := s.store.UpdateTreeDedupeShadow(ctx, w.id, func(raw []byte) ([]byte, error) {
		ledger, err := treededupe.Decode(raw)
		if err != nil {
			return nil, err // never overwrite a ledger this binary cannot read
		}
		for _, f := range apply {
			f(&ledger)
		}
		return ledger.Encode()
	})
	if err != nil {
		s.options.Logger.Warn("tree dedupe shadow dropped", "queue", w.Key.Repository, "base", w.Key.BaseBranch, "error", err)
	}
}

func (s *Server) treeDedupeBuild(ctx context.Context, w *worker, c *treeDedupeCapture, after queue.State, batchID, sha string, at time.Time, kind string) func(*treededupe.Ledger) {
	opts := s.options.TreeDedupeShadow
	build := treededupe.Build{At: at, BatchID: batchID, SHA: sha, Kind: kind, RetryConfigured: c.retry}
	if b := after.Batch(batchID); b != nil {
		build.Singleton = len(b.PRs) == 1
		switch {
		case b.FromBisection:
			build.Kind = treededupe.KindBisection
		case kind == treededupe.KindBatch && b.ParentBatchID != "":
			build.Kind = treededupe.KindChained
		}
	}
	in := treededupe.Inputs{
		Namespace: strings.Join([]string{w.Key.Installation, w.Key.Repository, w.Key.BaseBranch, w.Key.Lane}, "\x00"),
		Checks:    c.checks,
	}
	if opts.RunnerImage != nil {
		in.RunnerImage = opts.RunnerImage(w.Key)
	}
	switch reader, ok := w.Git.(treeDedupeInputsReader); {
	case opts.Inputs != nil:
		in.Git, in.GitErr = opts.Inputs(ctx, w.Key, sha)
	case ok:
		in.Git, in.GitErr = reader.TreeDedupeInputs(ctx, w.repo(), sha)
	default:
		in.GitErr = errTreeDedupeNoReader
	}
	key, unknown := treededupe.Key(in)
	build.Key, build.Unknown = key, treededupe.Unknowns(unknown)
	return func(l *treededupe.Ledger) { l.ObserveBuild(build) }
}

var errTreeDedupeNoReader = fmt.Errorf("tree dedupe: target git has no local tree reader")
