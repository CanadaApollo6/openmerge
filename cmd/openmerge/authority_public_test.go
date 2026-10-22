package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge/forgetest"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5"
)

func TestPublicAuthoritySetupVerifyConsultsReceipt(t *testing.T) {
	_, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	stop()
	if _, err := net.LookupHost("localhost"); err != nil {
		t.Fatal(err)
	}
	previous := authority.BuildRevision
	authority.BuildRevision = "public-command-test"
	t.Cleanup(func() { authority.BuildRevision = previous })
	revision := authority.RunningBinaryRevision()
	original := http.DefaultTransport
	calls := 0
	http.DefaultTransport = importTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected forge request")
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, kind := range []string{"binary", "expired", "inventory", "reconciliation", "backup"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rawURL := publicVerificationDatabaseURL(t)
				db, err := store.Open(t.Context(), rawURL)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(db.Close)
				q, err := db.EnsureQueue(t.Context(), store.QueueKey{Installation: "7", Repository: "owner/repo", BaseBranch: "main"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Append(t.Context(), q, []queue.Event{queue.BaseMoved{SHA: "receipt-genesis"}}, ""); err != nil {
					t.Fatal(err)
				}
				identity, err := db.DatabaseIdentity(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				backup := filepath.Join(t.TempDir(), "backup")
				if err := os.WriteFile(backup, []byte("public offline backup"), 0600); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				r := authority.EmissionReceipt{DatabaseIdentity: identity, BinaryRevision: revision, DrainedWorkers: []string{"worker"}, DrainedAt: now.Add(-time.Minute), RestoreTestedAt: now, BackupPath: backup, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("public offline backup"))), CompatibleReadersOnly: true}
				completeEmissionOperations(&r)

				switch kind {
				case "binary":
					r.BinaryRevision = "wrong"

				case "expired":
					r.DrainedAt = now.Add(-48 * time.Hour)

				case "inventory":
					r.Inventory = nil

				case "reconciliation":
					r.Reconciliation = nil

				case "backup":
					r.BackupPath += ".missing"

				}
				raw, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "receipt.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				var out, stderr bytes.Buffer
				code := run([]string{"setup", "rulesets", "--authority", "openmerge", "--verify", "--repo", "owner/repo", "--app-id", "5383059", "--operator", "operator", "--verification-plan", "must-not-read", "--emission-receipt", path, "--database-url", rawURL}, &out, &stderr)
				if code != 1 || !strings.Contains(stderr.String(), "emission receipt stale/invalid") || out.Len() != 0 || calls != 0 {
					t.Fatalf("public setup --verify skipped receipt refusal: exit=%d calls=%d stderr=%s", code, calls, &stderr)
				}
			})
		})
	}
}

func TestPublicAuthoritySetupVerifyRequiresLiveBackupWatermark(t *testing.T) {
	// Initialize process-wide signal delivery before the CLI enters a bubble.
	_, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	stop()
	if _, err := net.LookupHost("localhost"); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
		if raw == "" {
			t.Skip("set OPENMERGE_TEST_DATABASE_URL")
		}
		conn, err := pgx.Connect(t.Context(), raw)
		if err != nil {
			t.Fatal(err)
		}
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			t.Fatal(err)
		}
		schema := "public_verify_" + hex.EncodeToString(nonce[:])
		if _, err := conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
			_ = conn.Close(context.Background())
		})
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		query.Set("search_path", schema)
		u.RawQuery = query.Encode()
		db, err := store.Open(t.Context(), u.String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		input := forgetest.WriteRestrictionInput()
		key := store.QueueKey{Installation: strconv.FormatInt(input.Binding.InstallationID, 10), Repository: input.Binding.Repository, BaseBranch: "main"}
		input.Binding.QueueKey = store.AuthorityQueueKey(key)
		q, err := db.EnsureQueue(t.Context(), key)
		if err != nil {
			t.Fatal(err)
		}
		watermark, err := db.Append(t.Context(), q, []queue.Event{queue.BaseMoved{SHA: "base"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		identity, err := db.DatabaseIdentity(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		previous := authority.BuildRevision
		authority.BuildRevision = "public-watermark-test"
		t.Cleanup(func() { authority.BuildRevision = previous })
		dir := t.TempDir()
		backup := filepath.Join(dir, "backup")
		contents := []byte("isolated restore-tested backup")
		if err := os.WriteFile(backup, contents, 0600); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		receipt := authority.EmissionReceipt{DatabaseIdentity: identity, BinaryRevision: authority.RunningBinaryRevision(), DrainedWorkers: []string{"worker"}, DrainedAt: now.Add(-time.Minute), RestoreTestedAt: now, BackupPath: backup, BackupSHA256: fmt.Sprintf("%x", sha256.Sum256(contents)), CompatibleReadersOnly: true}
		completeEmissionOperations(&receipt)
		receipt.Reconciliation.BackupWatermark = watermark
		receipt.Reconciliation.RestoredWatermark = watermark
		if err := receipt.ValidateForBinary(identity, now, time.Hour, authority.RunningBinaryRevision()); err != nil {
			t.Fatal(err)
		}
		receiptPath := filepath.Join(dir, "receipt.json")
		planPath := filepath.Join(dir, "plan.json")
		for path, value := range map[string]any{receiptPath: receipt, planPath: verificationPlan{Targets: []verificationTarget{{Key: key, Input: input}}}} {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Append(t.Context(), q, []queue.Event{queue.BaseMoved{SHA: "unbacked-event"}}, ""); err != nil {
			t.Fatal(err)
		}
		original := http.DefaultTransport
		calls := 0
		http.DefaultTransport = importTestTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("unexpected forge read") })
		t.Cleanup(func() { http.DefaultTransport = original })
		t.Setenv("GITHUB_TOKEN", "mock-admin")
		var out, stderr bytes.Buffer
		code := run([]string{"setup", "rulesets", "--authority", "openmerge", "--verify", "--repo", key.Repository, "--app-id", input.AppID, "--operator", "operator", "--verification-plan", planPath, "--emission-receipt", receiptPath, "--database-url", u.String()}, &out, &stderr)
		if code != 1 || calls != 0 || !strings.Contains(stderr.String(), "backup watermark differs") {
			t.Fatalf("public setup --verify skipped live backup gate: exit=%d calls=%d stderr=%s", code, calls, &stderr)
		}
	})
}

// Each CLI invocation gets an isolated schema inside the caller's bubble.
func publicVerificationDatabaseURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("OPENMERGE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set OPENMERGE_TEST_DATABASE_URL")
	}
	conn, err := pgx.Connect(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "public_receipt_" + hex.EncodeToString(nonce[:])
	if _, err := conn.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		_ = conn.Close(context.Background())
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}
