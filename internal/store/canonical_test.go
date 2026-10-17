package store

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestCanonicalRegistrationReusesLiveQueue(t *testing.T) {
	db := openTestStore(t, databaseURL(t))
	key := QueueKey{Installation: "172166049", Repository: "CanadaApollo6/openmerge", BaseBranch: "main"}
	id, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), id, []queue.Event{queue.FenceInitialized{Token: "existing-fence"}}, "")
	must(t, err)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Go(func() {
			incoming := key
			incoming.Repository = strings.ToUpper(key.Repository)
			got, canonical, err := db.CanonicalQueue(t.Context(), incoming)
			if err != nil || got != id || canonical != key {
				t.Errorf("registration = %d %+v %v", got, canonical, err)
			}
		})
	}
	wg.Wait()
	keys, err := db.QueueKeys(t.Context())
	must(t, err)
	state, watermark, err := db.Load(t.Context(), id)
	must(t, err)
	if len(keys) != 1 || state.Fence != "existing-fence" || watermark != 1 {
		t.Fatalf("queue history changed: keys=%+v state=%+v watermark=%d", keys, state, watermark)
	}
}

func TestCanonicalNewQueueRegistrationRace(t *testing.T) {
	db := openTestStore(t, databaseURL(t))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Go(func() {
			name := "owner/repo"
			if i%2 == 0 {
				name = "Owner/Repo"
			}
			_, err := db.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: name, BaseBranch: "main"})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	keys, err := db.QueueKeys(t.Context())
	must(t, err)
	if len(keys) != 1 {
		t.Fatalf("racing registrations created %d queues", len(keys))
	}
}

func TestCanonicalSelectionSurvivesDrainAndReopen(t *testing.T) {
	url := databaseURL(t)
	db := openTestStore(t, url)
	key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	older, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	_, err = db.Append(t.Context(), older, []queue.Event{queue.FenceInitialized{Token: "stale"}}, "")
	must(t, err)
	var newer QueueID
	err = db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&newer)
	must(t, err)
	_, err = db.Append(t.Context(), newer, []queue.Event{queue.FenceInitialized{Token: "current"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()}}, "")
	must(t, err)
	must(t, db.Record(t.Context(), newer, "comment/123", []byte(`true`)))
	must(t, db.Record(t.Context(), newer, "cursor/1", []byte(`"2026-10-16T00:00:00Z"`)))
	// Model an upgrade without a cached choice. Fence ownership, rather than
	// the newer history's live entry, authorizes its selection.
	_, err = db.pool.Exec(t.Context(), `DELETE FROM canonical_queues`)
	must(t, err)
	got, canonical, err := db.CanonicalQueueWithFence(t.Context(), key, "current")
	must(t, err)
	if got != newer || canonical.Repository != "owner/repo" {
		t.Fatalf("wrong live selection: %d %+v", got, canonical)
	}
	state, _, err := db.Load(t.Context(), newer)
	must(t, err)
	_, err = db.Append(t.Context(), newer, []queue.Event{queue.Dequeued{PR: 1, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested}}, "")
	must(t, err)
	reopened := openTestStore(t, url)
	got, _, err = reopened.CanonicalQueue(t.Context(), key)
	must(t, err)
	if got != newer {
		t.Fatal("canonical selection reverted after drain and reopen")
	}
	state, _, err = reopened.Load(t.Context(), got)
	must(t, err)
	receipt, err := reopened.Observation(t.Context(), got, "comment/123")
	must(t, err)
	cursor, err := reopened.Observation(t.Context(), got, "cursor/1")
	must(t, err)
	if len(state.Entries) != 0 || state.Fence != "current" || string(receipt) != "true" || len(cursor) == 0 {
		t.Fatalf("canonical history lost: %+v receipt=%s cursor=%s", state, receipt, cursor)
	}
}

func TestCanonicalColdStartRequiresUniqueFenceOwner(t *testing.T) {
	for _, evidence := range []string{"current", "", "unknown", "shared"} {
		t.Run(evidence, func(t *testing.T) {
			db := openTestStore(t, databaseURL(t))
			key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
			var stale, owner QueueID
			must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','Owner/Repo','main','') RETURNING id`).Scan(&stale))
			must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&owner))
			staleFence, ownerFence := "stale", "current"
			if evidence == "shared" {
				staleFence, ownerFence = evidence, evidence
			}
			appendEvents(t, db, stale, queue.FenceInitialized{Token: staleFence})
			appendEvents(t, db, owner, queue.FenceInitialized{Token: ownerFence})
			// The stale variant logs last, as a lifecycle/access worker would.
			appendEvents(t, db, stale, queue.LifecycleChanged{State: "active"})
			got, _, err := db.CanonicalQueueWithFence(t.Context(), key, evidence)
			if evidence == "current" {
				must(t, err)
				if got != owner {
					t.Fatalf("selected stale fence history %d, want %d", got, owner)
				}
			} else {
				if !errors.Is(err, ErrFenceOwnershipRequired) {
					t.Fatalf("ambiguous fence accepted: id=%d err=%v", got, err)
				}
				var count int
				must(t, db.pool.QueryRow(t.Context(), `SELECT count(*) FROM canonical_queues`).Scan(&count))
				if count != 0 {
					t.Fatal("failed fence observation cached a choice")
				}
			}
		})
	}
}

func TestCanonicalVariantSetChangeRetainsCachedOwner(t *testing.T) {
	db := openTestStore(t, databaseURL(t))
	key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "main"}
	owner, err := db.EnsureQueue(t.Context(), key)
	must(t, err)
	appendEvents(t, db, owner, queue.FenceInitialized{Token: "owner"})
	var imported QueueID
	must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','owner/repo','main','') RETURNING id`).Scan(&imported))
	appendEvents(t, db, imported, queue.FenceInitialized{Token: "stale"}, queue.LifecycleChanged{State: "active"})
	got, _, err := db.CanonicalQueue(t.Context(), key)
	must(t, err)
	if got != owner {
		t.Fatalf("variant import flipped cached owner to %d", got)
	}
	// Import a third history with live state. Even fresh fence evidence must
	// not flip the existing choice or bypass that history's admission/holds.
	var competing QueueID
	must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ('7','OWNER/REPO','main','') RETURNING id`).Scan(&competing))
	appendEvents(t, db, competing, queue.FenceInitialized{Token: "competing"}, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()})
	if _, _, err := db.CanonicalQueueWithFence(t.Context(), key, "competing"); err == nil {
		t.Fatal("live imported history flipped cached choice")
	}
	var cached QueueID
	must(t, db.pool.QueryRow(t.Context(), `SELECT queue_id FROM canonical_queues`).Scan(&cached))
	if cached != owner {
		t.Fatal("conflict overwrote cached owner")
	}
}

func TestCanonicalUnfencedLiveVariantCannotBypassFencedReceipts(t *testing.T) {
	for _, live := range []string{"entry", "rules hold"} {
		t.Run(live, func(t *testing.T) {
			db := openTestStore(t, databaseURL(t))
			key := QueueKey{Installation: "7", Repository: "Owner/Repo", BaseBranch: "release/it's-ready", Lane: "batch"}
			var owner, unfenced QueueID
			must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ($1,$2,$3,$4) RETURNING id`, key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&owner))
			must(t, db.pool.QueryRow(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES ($1,lower($2),$3,$4) RETURNING id`, key.Installation, key.Repository, key.BaseBranch, key.Lane).Scan(&unfenced))
			appendEvents(t, db, owner, queue.FenceInitialized{Token: "existing"})
			must(t, db.Record(t.Context(), owner, "comment/123", []byte(`true`)))
			must(t, db.Record(t.Context(), owner, "cursor/1", []byte(`"2026-10-16T00:00:00Z"`)))
			if live == "entry" {
				appendEvents(t, db, unfenced, queue.PRObserved{PR: 1, HeadSHA: "head", Eligible: true, Trusted: true}, queue.EnqueueRequested{PR: 1, At: time.Now()})
			} else {
				appendEvents(t, db, unfenced, queue.RulesSourceBlocked{Source: queue.Rulesets, Problems: []string{"bootstrap rules hold"}})
			}
			for _, fence := range []string{"", "existing"} {
				got, _, err := db.CanonicalQueueWithFence(t.Context(), key, fence)
				if err == nil || !strings.Contains(err.Error(), "conflicting case-variant queue histories have live state") || got != 0 {
					t.Fatalf("unfenced history bypassed receipts: id=%d err=%v", got, err)
				}
				var count int
				must(t, db.pool.QueryRow(t.Context(), `SELECT count(*) FROM canonical_queues`).Scan(&count))
				if count != 0 {
					t.Fatal("conflict cached a choice")
				}
			}
			// Follow the documented drain/clear procedure before running its SQL.
			state, _, err := db.Load(t.Context(), unfenced)
			must(t, err)
			if live == "entry" {
				appendEvents(t, db, unfenced, queue.DequeueRequested{PR: 1}, queue.Dequeued{PR: 1, AdmissionID: state.Entries[0].AdmissionID, Reason: queue.Requested})
			} else {
				appendEvents(t, db, unfenced, queue.RulesSourceCleared{Source: queue.Rulesets, Basis: state.RulesSourceRevision(queue.Rulesets)})
			}
			doc, err := os.ReadFile("../../docs/operating.md")
			must(t, err)
			_, sql, ok := strings.Cut(string(doc), "```sql\n   BEGIN;")
			if !ok {
				t.Fatal("operator recovery SQL missing")
			}
			sql, _, _ = strings.Cut(sql, "```")
			escape := func(value string) string { return strings.ReplaceAll(value, "'", "''") }
			sql = strings.NewReplacer("INSTALLATION", escape(key.Installation), "OWNER/REPO", escape(key.Repository), "BASE", escape(key.BaseBranch), "LANE", escape(key.Lane), "SELECTED_ID", fmt.Sprint(owner)).Replace("BEGIN;" + sql)
			_, err = db.pool.Exec(t.Context(), sql)
			must(t, err)
			got, canonical, err := db.CanonicalQueue(t.Context(), key)
			must(t, err)
			if got != owner || canonical != key {
				t.Fatalf("operator SQL selected %d %+v, want %d %+v", got, canonical, owner, key)
			}
			receipt, err := db.Observation(t.Context(), got, "comment/123")
			must(t, err)
			cursor, err := db.Observation(t.Context(), got, "cursor/1")
			must(t, err)
			state, _, err = db.Load(t.Context(), got)
			must(t, err)
			other, _, err := db.Load(t.Context(), unfenced)
			must(t, err)
			if state.Fence != "existing" || string(receipt) != "true" || len(cursor) == 0 || other.Fence != "" {
				t.Fatalf("recovery lost history: owner=%+v other=%+v receipt=%s cursor=%s", state, other, receipt, cursor)
			}
		})
	}
}
