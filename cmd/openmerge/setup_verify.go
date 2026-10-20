package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/forge/github"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/CanadaApollo6/openmerge/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Prepared targets use existing internal contracts. This file is an operator
// artifact; it is never accepted by the repository's public config loader.
type verificationTarget struct {
	Key   store.QueueKey              `json:"queue"`
	Input forge.WriteRestrictionInput `json:"input"`
}
type verificationPlan struct {
	Targets []verificationTarget `json:"targets"`
}
type verifyOptions struct {
	Verify                               bool
	ReceiptMaxAge                        time.Duration
	DatabaseURL, Plan, Receipt, Operator string
}

func readVerificationJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, 4<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one verification JSON document")
	}
	return nil
}

func verificationDatabaseIdentity(raw string) (string, error) {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return "", errors.New("invalid operator database URL")
	}
	c := cfg.ConnConfig
	return fmt.Sprintf("%s:%d/%s?search_path=%s", c.Host, c.Port, c.Database, c.RuntimeParams["search_path"]), nil
}

func runRulesVerification(ctx context.Context, opts verifyOptions, rules github.RulesOptions, out io.Writer) error {
	if rules.Apply || rules.Restore != "" || rules.RollbackPreflight || rules.RotateBreakGlass {
		return errors.New("--verify cannot be combined with --apply, --restore, --rollback-preflight or --rotate-break-glass")
	}
	if rules.Authority != "openmerge" || !opts.Verify || opts.Operator == "" || len(opts.Operator) > 256 || opts.Plan == "" || opts.Receipt == "" || opts.DatabaseURL == "" {
		return errors.New("--authority openmerge --verify requires --verification-plan, --emission-receipt, --operator and --database-url")
	}
	identity, err := verificationDatabaseIdentity(opts.DatabaseURL)
	if err != nil {
		return err
	}
	var receipt authority.EmissionReceipt
	if err := readVerificationJSON(opts.Receipt, &receipt); err != nil {
		return err
	}
	if err := receipt.ValidateForBinary(identity, time.Now(), opts.ReceiptMaxAge, authority.RunningBinaryRevision()); err != nil {
		return err
	}
	var plan verificationPlan
	if err := readVerificationJSON(opts.Plan, &plan); err != nil {
		return err
	}
	if len(plan.Targets) == 0 || len(plan.Targets) > 1000 {
		return errors.New("verification plan requires 1..1000 targets")
	}
	seen := map[string]bool{}
	for _, t := range plan.Targets {
		b := t.Input.Binding
		if err := forge.ValidateWriteRestrictionInput(t.Input); err != nil {
			return err
		}
		key := store.AuthorityQueueKey(t.Key)
		if !slices.Contains(t.Input.ExpectedRefs, "refs/heads/"+t.Key.BaseBranch) || !slices.Contains(t.Input.ExpectedRefs, "refs/heads/"+b.DefaultBranch) || seen[key] || t.Key.Lane != "" || t.Key.Installation != strconv.FormatInt(b.InstallationID, 10) || t.Key.Repository != rules.Repo || b.Repository != t.Key.Repository || b.QueueKey != key || rules.AppID <= 0 || t.Input.AppID != strconv.FormatInt(rules.AppID, 10) {
			return errors.New("verification plan queue/repository/App binding mismatch or duplicate target")
		}
		seen[key] = true
	}
	reader, err := github.NewAdminWriteRestrictionReader(os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, opts.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	return verifyTargets(ctx, db, reader, plan, opts.Operator, out)
}

// Generate the invocation nonce before any seeing read. A fresh invocation
// never dedupes identical daily fingerprints. Callers retry Commit with these
// exact records; a new read must create a new invocation and nonce.
func verifyTargets(ctx context.Context, db *store.Store, reader forge.WriteRestrictionReader, plan verificationPlan, operator string, out io.Writer) error {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	nonce := hex.EncodeToString(random[:])
	stored, err := db.StoredQueues(ctx)
	if err != nil {
		return err
	}
	records := make([]store.VerificationRecord, 0, len(plan.Targets))
	// Resolve and validate every target before performing any network read.
	for _, target := range plan.Targets {
		var id store.QueueID
		for _, q := range stored {
			if q.Key == target.Key {
				id = q.ID
				break
			}
		}
		if id == 0 {
			return errors.New("verification queue is not registered")
		}
		state, _, err := db.Load(ctx, id)
		if err != nil {
			return err
		}
		scope := "proposed"
		if pin := state.AuthorityPin(); pin != nil && pin.Authority == authority.OpenMerge {
			scope = "applied"
			if pin.OpenMerge == nil || !reflect.DeepEqual(pin.OpenMerge.Binding, target.Input.Binding) || state.ConfigDigest != target.Input.Binding.EffectiveDigest {
				return errors.New("applied verification plan differs from pinned policy")
			}
		}
		records = append(records, store.VerificationRecord{QueueID: id, Event: queue.WriteRestrictionVerified{Binding: target.Input.Binding, Basis: state.RulesSourceRevision(queue.WriteRestriction), Scope: scope, Nonce: nonce, Operator: operator}})
	}
	for i, target := range plan.Targets {
		evidence, err := reader.ObserveWriteRestriction(ctx, target.Input)
		if err != nil {
			return fmt.Errorf("seeing read failed; no verification recorded: %w", err)
		}
		evidence, err = forge.InterpretWriteRestriction(target.Input, evidence)
		if err != nil {
			return err
		}
		fp, err := evidence.Fingerprint()
		if err != nil {
			return fmt.Errorf("incomplete seeing read; no verification recorded: %w", err)
		}
		records[i].Event.Evidence = evidence
		records[i].Event.ProtectionFingerprint = fp
		records[i].Event.VerifiedAt = time.Now().UTC()
	}
	receipts, err := db.CommitWriteRestrictionVerification(ctx, records)
	if err != nil {
		return err
	}
	violated := false
	stale := false
	for _, r := range receipts {
		state, _, err := db.Load(ctx, r.QueueID)
		if err != nil {
			return err
		}
		status := state.WriteRestrictionStatus()
		if r.Event.Scope == "applied" && len(r.Event.Evidence.Findings) == 0 && !status.LastVerifiedAt.Equal(r.Event.VerifiedAt) {
			stale = true
		}
		if len(r.Event.Evidence.Findings) > 0 {
			violated = true
		}
		if _, err := fmt.Fprintf(out, "queue %s: write_restriction_verified event %d scope=%s findings=%d seeing_read_at=%s last_observed_at=%s last_verified_at=%s state=%s\n", r.Event.Binding.QueueKey, r.EventID, r.Event.Scope, len(r.Event.Evidence.Findings), r.Event.VerifiedAt.Format(time.RFC3339Nano), status.LastObservedAt.Format(time.RFC3339Nano), status.LastVerifiedAt.Format(time.RFC3339Nano), status.State); err != nil {
			return err
		}
	}
	if stale {
		return errors.New("verification evidence recorded but basis became stale; verification time did not advance for this read; retry --verify with a fresh seeing read")
	}
	if violated {
		return errors.New("visible write-restriction violation recorded")
	}
	return nil
}
