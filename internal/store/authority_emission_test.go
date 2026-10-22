package store

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestAuthorityEmissionLiveBackupWatermark(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		id, err := s.EnsureQueue(t.Context(), QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"})
		must(t, err)
		appendEvents(t, s, id, queue.BaseMoved{SHA: "base"})
		events, err := s.EventsAfter(t.Context(), id, 0)
		must(t, err)
		watermark := events[len(events)-1].ID
		must(t, s.CheckAuthorityEmissionBackup(t.Context(), watermark))
		appendEvents(t, s, id, queue.BaseMoved{SHA: "outside"})
		if err := s.CheckAuthorityEmissionBackup(t.Context(), watermark); err == nil || !strings.Contains(err.Error(), "watermark") {
			t.Fatalf("stale live backup accepted: %v", err)
		}
		// A legacy acknowledgement cannot waive the stronger authority gate.
		appendEvents(t, s, id, queue.OutsidePushAcknowledged{SourceEventID: watermark, SHA: "outside", Operator: "operator", At: time.Now().UTC()})
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if boundary {
			t.Fatal("legacy acknowledgement waived authority emission receipt")
		}
		if err := s.CheckAuthorityEmissionBackup(t.Context(), watermark); err == nil {
			t.Fatal("acknowledgement waived stale backup watermark")
		}
		// A real retained boundary means subsequent compatible emissions use the
		// original tested pre-emission backup, rather than inventing a newer one.
		raw, err := os.ReadFile("../queue/testdata/wire/v1/event_write_restriction_verified.json")
		must(t, err)
		event, err := queue.DecodeEvent(raw)
		must(t, err)
		appendEvents(t, s, id, event)
		must(t, s.CheckAuthorityEmissionBackup(t.Context(), watermark))
	})
}

func TestAuthorityEmissionReceiptBoundary(t *testing.T) {
	_, dnsErr := net.LookupHost("localhost")
	must(t, dnsErr)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		stale := errors.New("emission receipt stale/invalid")
		calls := 0
		gate := AuthorityEmissionGate{Watermark: 0, Validate: func() error { calls++; return stale }}
		if _, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate); !errors.Is(err, stale) {
			t.Fatalf("first emission ignored invalid receipt: %v", err)
		}
		events, err := s.EventsAfter(t.Context(), q, 0)
		must(t, err)
		if len(events) != 0 || calls != 1 {
			t.Fatal("invalid first receipt emitted event", len(events), calls)
		}
		gate.Validate = func() error { calls++; return nil }
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate)
		must(t, err)
		v.Nonce = "after-boundary"
		gate.Watermark = -1
		gate.Validate = func() error { t.Fatal("post-boundary emission consulted receipt"); return stale }
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate)
		must(t, err)
	})
}

func TestAuthorityEmissionSerializesAllQueueAppends(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		k.BaseBranch = "release/other"
		other, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		appended := make(chan error, 1)
		gate := AuthorityEmissionGate{Watermark: 0, Validate: func() error {
			go func() {
				_, err := s.Append(t.Context(), other, []queue.Event{queue.BaseMoved{SHA: "concurrent"}}, "")
				appended <- err
			}()
			// Observe the actual PostgreSQL lock wait, not wall-clock scheduling. The
			// contender can finish freely when the global lock is removed by mutation.
			for i := 0; i < 10000; i++ {
				select {
				case err := <-appended:
					t.Fatalf("other queue append escaped first-emission lock: %v", err)
				default:
				}
				var blocked bool
				err := s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks l WHERE l.relation='events'::regclass AND l.mode='RowExclusiveLock' AND NOT l.granted)`).Scan(&blocked)
				if err != nil {
					return err
				}
				if blocked {
					return nil
				}
			}
			return errors.New("other queue append never reached global lock")
		}}
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate)
		must(t, err)
		must(t, <-appended)
	})
}

func TestAuthorityEmissionRefreshesSnapshotAfterAppendLockWait(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		raw, err := url.Parse(databaseURL(t))
		must(t, err)
		params := raw.Query()
		params.Set("default_transaction_isolation", "repeatable read")
		raw.RawQuery = strings.ReplaceAll(params.Encode(), "+", "%20")
		s := openTestStore(t, raw.String())
		var defaultIsolation string
		must(t, s.pool.QueryRow(t.Context(), `SHOW default_transaction_isolation`).Scan(&defaultIsolation))
		if defaultIsolation != "repeatable read" {
			t.Fatal("fixture did not override isolation default", defaultIsolation)
		}
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		k.BaseBranch = "release/other"
		other, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		holder, err := s.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		must(t, err)
		defer func() { _ = holder.Rollback(t.Context()) }()
		name, payload, err := queue.EncodeEvent(queue.BaseMoved{SHA: "unbacked-concurrent-append"})
		must(t, err)
		_, err = holder.Exec(t.Context(), `INSERT INTO events(queue_id,type,payload) VALUES($1,$2,$3::jsonb)`, other, name, string(payload))
		must(t, err)
		finished := make(chan error, 1)
		go func() {
			_, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0})
			finished <- err
		}()
		waiting := false
		for i := 0; i < 10000; i++ {
			select {
			case err := <-finished:
				t.Fatalf("first emission did not wait for concurrent append: %v", err)
			default:
			}
			must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks l WHERE l.relation='events'::regclass AND l.mode='ShareRowExclusiveLock' AND NOT l.granted)`).Scan(&waiting))
			if waiting {
				break
			}
		}
		if !waiting {
			t.Fatal("first emission never reached global append lock")
		}
		must(t, holder.Commit(t.Context()))
		if err := <-finished; err == nil || !strings.Contains(err.Error(), "watermark") {
			t.Fatalf("first emission used a stale pre-lock journal snapshot: %v", err)
		}
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if boundary {
			t.Fatal("unbacked concurrent append allowed first boundary")
		}
	})
}
