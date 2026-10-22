package store

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"testing/synctest"

	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

func requireTenancyRefusal(t *testing.T, err error) {
	t.Helper()
	var tenancy *AuthorityTenancyError
	if !errors.As(err, &tenancy) || tenancy.Installations != 2 || err.Error() != "authority mode requires a dedicated database per installation: this database serves 2 installations" {
		t.Fatalf("expected sanitized tenancy refusal: %v", err)
	}
}

func TestAuthorityTenancyEmission(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		// Multiple repositories/partitions within one installation pass.
		other := k
		other.Repository = "private/other"
		other.BaseBranch = "excluded"
		_, err = s.EnsureQueue(t.Context(), other)
		must(t, err)
		must(t, s.CheckAuthorityTenancy(t.Context()))
		must(t, s.CheckAuthorityEmissionBackup(t.Context(), 0))
		other.Installation = "second-private-installation"
		hidden, err := s.EnsureQueue(t.Context(), other)
		must(t, err)
		for _, transition := range []queue.Event{nil, queue.PauseRequested{Reason: "test", Detail: "hidden"}, queue.LifecycleChanged{State: "retired", Detail: "hidden"}} {
			if transition != nil {
				appendEvents(t, s, hidden, transition)
			}
			requireTenancyRefusal(t, s.CheckAuthorityTenancy(t.Context()))
			requireTenancyRefusal(t, s.CheckAuthorityEmissionBackup(t.Context(), 0))
			_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0})
			requireTenancyRefusal(t, err)
		}
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if boundary {
			t.Fatal("tenancy refusal emitted boundary")
		}
	})
}

func TestAuthorityTenancyConcurrentRegistration(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		holder, err := s.pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		must(t, err)
		defer func() { _ = holder.Rollback(t.Context()) }()
		other := k
		other.Installation = "second"
		must(t, s.lockRegistration(t.Context(), holder, other))
		registered := make(chan error, 1)
		go func() { _, err := s.EnsureQueue(t.Context(), other); registered <- err }()
		waitTenancyLock(t, s, "ShareLock", true)
		finished := make(chan error, 1)
		go func() {
			_, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0})
			finished <- err
		}()
		waitTenancyLock(t, s, "ExclusiveLock", false)
		select {
		case err := <-finished:
			t.Fatalf("emission escaped pending registration: %v", err)
		default:
		}
		var eventLock bool
		must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE relation='events'::regclass AND mode='ShareRowExclusiveLock' AND granted)`).Scan(&eventLock))
		if eventLock {
			t.Fatal("emission acquired events before tenancy")
		}
		must(t, holder.Commit(t.Context()))
		must(t, <-registered)
		requireTenancyRefusal(t, <-finished)
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if boundary {
			t.Fatal("concurrent registration allowed boundary")
		}
	})
}

func TestAuthorityTenancyRegistrationWaitsForEmission(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		registered := make(chan error, 1)
		gate := AuthorityEmissionGate{Watermark: 0, Validate: func() error {
			go func() {
				k.Installation = "second"
				_, err := s.EnsureQueue(t.Context(), k)
				registered <- err
			}()
			for i := 0; i < 10000; i++ {
				select {
				case err := <-registered:
					t.Fatalf("registration escaped boundary lock: %v", err)
				default:
				}
				var waiting bool
				must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=1330464084 AND mode='ShareLock' AND NOT granted)`).Scan(&waiting))
				if waiting {
					return nil
				}
			}
			return errors.New("registration never reached boundary lock")
		}}
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate)
		must(t, err)
		must(t, <-registered)
		requireTenancyRefusal(t, s.CheckAuthorityTenancy(t.Context()))
	})
}

// Poll actual database synchronization, with a fixed bound and no sleeps.
func waitTenancyLock(t *testing.T, s *Store, mode string, granted bool) {
	t.Helper()
	for range 10000 {
		var found bool
		must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=1330464084 AND objid=hashtext($1)::oid AND mode=$2 AND granted=$3)`, s.testLockScope, mode, granted).Scan(&found))
		if found {
			return
		}
	}
	t.Fatalf("tenancy %s granted=%v not observed", mode, granted)
}

func TestAuthorityTenancyNotificationOverlap(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		locked, release := make(chan struct{}), make(chan struct{})
		s.beforeNotificationUpdate = func() { close(locked); <-release }
		notified := make(chan error, 1)
		go func() { _, err := s.TakeNotification(t.Context(), q); notified <- err }()
		<-locked
		// Astra's interleaving: real TakeNotification holds FOR UPDATE while
		// first emission takes its locks; release the UPDATE at the validation seam.
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0, Validate: func() error { close(release); return nil }})
		notificationErr := <-notified
		if err != nil || notificationErr != nil {
			t.Fatalf("emission=%v notification=%v", err, notificationErr)
		}
	})
}

func TestAuthorityTenancyConcurrentWriters(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	synctest.Test(t, func(t *testing.T) {

		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		// A pending real registration already owns shared tenancy. First emission
		// must wait for it, then refuse the second installation, even under load.
		holder, err := s.pool.Begin(t.Context())
		must(t, err)
		defer func() { _ = holder.Rollback(t.Context()) }()
		other := k
		other.Installation = "second"
		must(t, s.lockRegistration(t.Context(), holder, other))
		done := make(chan error, 5)
		go func() { _, err := s.EnsureQueue(t.Context(), other); done <- err }()
		waitTenancyLock(t, s, "ShareLock", true)
		emitted := make(chan error, 1)
		go func() {
			_, err := s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0})
			emitted <- err
		}()
		waitTenancyLock(t, s, "ExclusiveLock", false)
		start := make(chan struct{})
		for worker := range 4 {
			go func() {
				<-start
				for i := range 12 {
					var err error
					switch worker {
					case 0:
						_, err = s.TakeNotification(t.Context(), q)
					case 1:
						err = s.Notify(t.Context(), q, fmt.Sprintf("stress-%d", i))
					case 2:
						key := k
						key.Lane = fmt.Sprintf("lane-%d", i)
						_, err = s.EnsureQueue(t.Context(), key)
					case 3:
						_, err = s.Append(t.Context(), q, []queue.Event{queue.PauseRequested{Reason: "test", Detail: "stress"}}, "")
					}
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
		}
		close(start)
		must(t, holder.Commit(t.Context()))
		for range 5 {
			must(t, <-done)
		}
		requireTenancyRefusal(t, <-emitted)
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if boundary {
			t.Fatal("racing second installation allowed boundary")
		}
	})
}

func TestAuthorityTenancyBoundaryDefense(t *testing.T) {
	s := newStore(t)
	k, v, _ := verificationFixture(t)
	q, err := s.EnsureQueue(t.Context(), k)
	must(t, err)
	_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, AuthorityEmissionGate{Watermark: 0})
	must(t, err)
	k.Installation = "second"
	_, err = s.EnsureQueue(t.Context(), k)
	must(t, err)
	// Explicit gate callers still get a tenancy check with a boundary present.
	requireTenancyRefusal(t, s.CheckAuthorityEmissionBackup(t.Context(), 0))
}

func TestAuthorityTenancyConcurrentWritersSuccess(t *testing.T) {
	_, err := net.LookupHost("localhost")
	must(t, err)
	// Also exercise a successful boundary INSERT under the same writer load,
	// with registration and append held behind its locks. Notifications must
	// finish while the emitter is still inside its validation seam.
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		k, v, _ := verificationFixture(t)
		q, err := s.EnsureQueue(t.Context(), k)
		must(t, err)
		done := make(chan error, 4)
		gate := AuthorityEmissionGate{Watermark: 0, Validate: func() error {
			for worker := range 4 {
				go func() {
					for i := range 12 {
						var err error
						switch worker {
						case 0:
							_, err = s.TakeNotification(t.Context(), q)
						case 1:
							err = s.Notify(t.Context(), q, fmt.Sprintf("success-%d", i))
						case 2:
							key := k
							key.Lane = fmt.Sprintf("success-%d", i)
							_, err = s.EnsureQueue(t.Context(), key)
						case 3:
							_, err = s.Append(t.Context(), q, []queue.Event{queue.PauseRequested{Reason: "test", Detail: "stress"}}, "")
						}
						if err != nil {
							done <- err
							return
						}
					}
					done <- nil
				}()
			}
			waitTenancyLock(t, s, "ShareLock", false)
			for range 2 {
				must(t, <-done)
			}
			return nil
		}}
		_, err = s.CommitWriteRestrictionVerification(t.Context(), []VerificationRecord{{q, v}}, gate)
		must(t, err)
		for range 2 {
			must(t, <-done)
		}
		boundary, err := s.AuthorityBoundaryExists(t.Context())
		must(t, err)
		if !boundary {
			t.Fatal("successful emission lost boundary")
		}
	})
}
