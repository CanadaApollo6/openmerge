package store

import (
	"errors"
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
		_, err = holder.Exec(t.Context(), `INSERT INTO queues(installation,repository,base,lane) VALUES('second','other/private','excluded','')`)
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
				t.Fatalf("emission escaped pending registration: %v", err)
			default:
			}
			must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE relation='queues'::regclass AND mode='ShareLock' AND NOT granted)`).Scan(&waiting))
			if waiting {
				break
			}
		}
		if !waiting {
			t.Fatal("emission never synchronized with queue registration")
		}
		must(t, holder.Commit(t.Context()))
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
				must(t, s.pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE relation='queues'::regclass AND mode='RowExclusiveLock' AND NOT granted)`).Scan(&waiting))
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
