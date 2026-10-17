package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/google/go-github/v93/github"
)

type AuditViolation struct {
	EventID int64     `json:"event_id"`
	At      time.Time `json:"at"`
	Check   string    `json:"check"`
	Message string    `json:"message"`
}
type AuditResult struct {
	Version         int              `json:"version"`
	Queue           store.QueueKey   `json:"queue"`
	Since           time.Time        `json:"since"`
	Until           time.Time        `json:"until"`
	CheckedEventIDs []int64          `json:"checked_event_ids"`
	Violations      []AuditViolation `json:"violations"`
}

// gitReader issues object/ancestry reads only. It neither initializes nor fetches
// repositories, including when passed a server-owned bare cache directory.
type gitReader struct{ dir string }

func (g gitReader) command(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "--no-replace-objects", "-C", g.dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
func validSHA(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil && (len(s) == 40 || len(s) == 64)
}
func (g gitReader) commit(ctx context.Context, sha string) error {
	if !validSHA(sha) {
		return fmt.Errorf("invalid full commit SHA %q", sha)
	}
	out, err := g.command(ctx, "rev-parse", "--verify", sha+"^{commit}")
	if err == nil && out != sha {
		return fmt.Errorf("object %s is not the recorded commit", sha)
	}
	return err
}
func (g gitReader) forward(ctx context.Context, old, head string) error {
	if old == "" || old == head {
		return fmt.Errorf("missing predecessor or main did not advance (%s -> %s)", old, head)
	}
	if err := g.commit(ctx, old); err != nil {
		return err
	}
	if err := g.commit(ctx, head); err != nil {
		return err
	}
	_, err := g.command(ctx, "merge-base", "--is-ancestor", old, head)
	return err
}
func (g gitReader) tested(ctx context.Context, b *queue.Batch, head string) error {
	if b == nil || b.TestedSHA == "" {
		return fmt.Errorf("no recorded tested batch")
	}
	if err := g.commit(ctx, b.TestedSHA); err != nil {
		return err
	}
	if err := g.commit(ctx, head); err != nil {
		return err
	}
	if b.Method != queue.FastForward {
		if head != b.TestedSHA {
			return fmt.Errorf("landed %s differs from tested %s", head, b.TestedSHA)
		}
		return nil
	}
	if len(b.PRs) == 0 || head != b.PRs[len(b.PRs)-1].HeadSHA || head != b.LandingAttempt.FastForwardHeadSHA {
		return fmt.Errorf("fast-forward head differs from pinned landing attempt")
	}
	shape, err := g.command(ctx, "show", "-s", "--format=%P%n%T", b.TestedSHA)
	if err != nil {
		return err
	}
	tree, err := g.command(ctx, "rev-parse", head+"^{tree}")
	if err != nil {
		return err
	}
	parents, markerTree, _ := strings.Cut(shape, "\n")
	if parents != head || markerTree != tree {
		return fmt.Errorf("fast-forward tested marker must have exactly the landed head as parent and the same tree")
	}
	return nil
}
func collectLandings(j store.Journal) []landing {
	s := j.Seed
	var result []landing
	for _, r := range j.Events {
		before := s
		s = queue.Apply(s, r.Event)
		if e, ok := r.Event.(queue.Landed); ok {
			b := before.Batch(e.BatchID)
			previous := before.BaseSHA
			if b != nil {
				previous = b.BaseSHA
			}
			result = append(result, landing{Record: r, Previous: previous, Observed: before.BaseSHA, Batch: b, Accepted: b != nil && b.Phase == queue.Landing && s.Batch(e.BatchID) == nil})
		}
	}
	return result
}
func audit(ctx context.Context, j store.Journal, since, until time.Time, g gitReader, a Actions) (AuditResult, error) {
	result := AuditResult{Version: 1, Queue: j.Key, Since: since, Until: until}
	if since.IsZero() || !until.After(since) {
		return result, fmt.Errorf("audit needs a positive time window")
	}
	shallow, err := g.command(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return result, err
	}
	if shallow != "false" {
		return result, fmt.Errorf("audit needs a complete, non-shallow repository")
	}
	success := map[string][]*github.WorkflowRun{}
	var previousQueueHead string
	for _, l := range collectLandings(j) {
		e := l.Record.Event.(queue.Landed)
		previous := previousQueueHead
		previousQueueHead = e.NewBaseSHA
		if l.Record.RecordedAt.Before(since) || !l.Record.RecordedAt.Before(until) {
			continue
		}
		id := l.Record.ID
		result.CheckedEventIDs = append(result.CheckedEventIDs, id)
		violation := func(check string, err error) {
			if err != nil {
				result.Violations = append(result.Violations, AuditViolation{EventID: id, At: l.Record.RecordedAt, Check: check, Message: err.Error()})
			}
		}
		if !l.Accepted {
			violation("journal", fmt.Errorf("landed event %d is not a valid replay transition", id))
		}
		violation("tested_commit", g.tested(ctx, l.Batch, e.NewBaseSHA))
		// The tested base is authoritative even if a forge observation already
		// reported our own push. Also check an intervening external head and the
		// previous queue landing, including one before --since.
		violation("main_forward", g.forward(ctx, l.Previous, e.NewBaseSHA))
		if l.Observed != "" && l.Observed != l.Previous && l.Observed != e.NewBaseSHA {
			violation("main_forward", g.forward(ctx, l.Observed, e.NewBaseSHA))
		}
		if previous != "" && previous != l.Previous && previous != l.Observed {
			violation("main_forward", g.forward(ctx, previous, e.NewBaseSHA))
		}
		if l.Batch == nil || l.Batch.TestedSHA == "" {
			violation("ci_success", fmt.Errorf("no tested commit to query"))
			continue
		}
		sha := l.Batch.TestedSHA
		runs, ok := success[sha]
		if !ok {
			listed, err := a.List(ctx, sha)
			if err != nil {
				return result, err
			}
			runs, err = attempts(ctx, a, listed)
			if err != nil {
				return result, err
			}
			success[sha] = runs
		}
		passed := false
		for _, r := range runs {
			if r.GetHeadSHA() == sha && r.GetStatus() == "completed" && r.GetConclusion() == "success" && !r.GetUpdatedAt().IsZero() && !r.GetUpdatedAt().After(l.Record.RecordedAt) {
				passed = true
			}
		}
		if !passed {
			violation("ci_success", fmt.Errorf("CI has no successful queue-ref attempt on %s completed by landing time", sha))
		}
	}
	return result, nil
}
func attachAudit(r *Report, j store.Journal, a AuditResult) error {
	if a.Version != 1 || a.Queue != r.Queue || a.Since.After(r.Window.From.Time) || a.Until.Before(r.Window.To.Time) {
		return fmt.Errorf("audit does not cover this queue and the complete report window")
	}
	checked := map[int64]bool{}
	for _, id := range a.CheckedEventIDs {
		checked[id] = true
	}
	for _, rec := range j.Events {
		if _, ok := rec.Event.(queue.Landed); ok && r.Window.contains(rec) && !checked[rec.ID] {
			return fmt.Errorf("audit is missing landed event %d", rec.ID)
		}
	}
	// Keep only this period's evidence when a daily audit covers a wider range.
	a.Since, a.Until = r.Window.From.Time, r.Window.To.Time
	a.CheckedEventIDs = nil
	a.Violations = append([]AuditViolation(nil), a.Violations...)
	a.Violations = slicesInWindow(a.Violations, r.Window)
	for _, rec := range j.Events {
		if checked[rec.ID] && r.Window.contains(rec) {
			a.CheckedEventIDs = append(a.CheckedEventIDs, rec.ID)
		}
	}
	r.Audit = &a
	return nil
}
func slicesInWindow(violations []AuditViolation, w Window) []AuditViolation {
	var result []AuditViolation
	for _, v := range violations {
		if w.contains(store.JournalEvent{ID: v.EventID, RecordedAt: v.At}) {
			result = append(result, v)
		}
	}
	return result
}
