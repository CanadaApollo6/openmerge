package store

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"github.com/jackc/pgx/v5"
)

// AuthorityCompletionRequest identifies the operator's intended policy, never a
// digest inferred from whichever queue happens to answer first. Targets overrides
// Digest per base when effective queue policies differ within a repository.
type AuthorityCompletionRequest struct {
	Installation string            `json:"installation"`
	Repository   string            `json:"repo"`
	Digest       string            `json:"digest"`
	Targets      map[string]string `json:"targets,omitempty"`
}

// Validate rejects incomplete intent before any discovery or database I/O.
func (r AuthorityCompletionRequest) Validate() error {
	if r.Installation == "" || r.Repository == "" || len(r.Targets) > 1000 || r.Digest != "" && !authority.IsDigest(r.Digest) || r.Digest == "" && len(r.Targets) == 0 {
		return errors.New("authority completion requires installation, repository and expected digests")
	}
	for base, digest := range r.Targets {
		if base == "" || !authority.IsDigest(digest) {
			return errors.New("invalid authority completion target")
		}
	}
	return nil
}

// AuthorityQueueCompletion explains one registered or expected partition.
type AuthorityQueueCompletion struct {
	Installation    string  `json:"installation"`
	Watermark       int64   `json:"watermark"`
	GoverningCommit string  `json:"governing_commit"`
	ID              QueueID `json:"id"`
	Base            string  `json:"base"`
	Lane            string  `json:"lane"`
	Digest          string  `json:"digest"`
	ExpectedDigest  string  `json:"expected_digest"`
	Reason          string  `json:"reason"`
}

// AuthorityCompletion is a point-in-time signal, not a durable permission to
// remove protection. Empty, mixed, unverified and undiscovered partitions fail
// closed. Per-queue adoption receipts cannot substitute for RequireComplete.
type AuthorityCompletion struct {
	Request  AuthorityCompletionRequest `json:"request"`
	Complete bool                       `json:"complete"`
	Queues   []AuthorityQueueCompletion `json:"queues"`
}

// RequireComplete is the fail-closed gate consumed by the final public switch.
func (c AuthorityCompletion) RequireComplete() error {
	if !c.Complete || len(c.Queues) == 0 {
		return errors.New("repository authority propagation incomplete; retain redundant protection")
	}
	return nil
}

// RepositoryAuthorityCompletion folds ALL registered partitions for this
// repository across installations in one read-only repeatable-read snapshot, regardless
// of worker allowlists, pause, retirement or outstanding obligations. liveBases
// must be a complete fresh discovery inventory, not a worker-key snapshot. The
// caller must recheck that inventory after this read. No result is cached.
func (s *Store) RepositoryAuthorityCompletion(ctx context.Context, request AuthorityCompletionRequest, liveBases []string) (AuthorityCompletion, error) {
	result := AuthorityCompletion{Request: request, Queues: []AuthorityQueueCompletion{}}
	if err := request.Validate(); err != nil {
		return result, err
	}
	if len(liveBases) == 0 {
		return result, errors.New("authority completion requires complete live branch inventory")
	}
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,installation,base,lane FROM queues WHERE lower(repository)=lower($1) ORDER BY id`, request.Repository)
		if err != nil {
			return err
		}
		partitions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AuthorityQueueCompletion, error) {
			var q AuthorityQueueCompletion
			err := row.Scan(&q.ID, &q.Installation, &q.Base, &q.Lane)
			return q, err
		})
		if err != nil {
			return err
		}
		expected := map[string]bool{}
		for base := range request.Targets {
			expected[base] = true
		}
		for _, q := range partitions {
			expected[q.Base] = true
			state, watermark, err := load(ctx, tx, q.ID)
			if err != nil {
				return err
			}
			q.Watermark = watermark
			q.ExpectedDigest = request.Digest
			if d, ok := request.Targets[q.Base]; ok {
				q.ExpectedDigest = d
			}
			q.Digest = state.ConfigDigest
			q.Reason = "pin_missing"
			inputsCurrent := true
			pin := state.AuthorityPin()
			if pin != nil {
				q.GoverningCommit = pin.GoverningCommit
			}
			if pin != nil && pin.OpenMerge != nil && pin.Authority == authority.OpenMerge {
				if pin.OpenMerge.VerificationInput != nil {
					if !slices.Contains(liveBases, pin.DefaultBranch) {
						inputsCurrent = false
					}
					coverage, err := authority.CompileRefCoverage(pin.DefaultBranch, pin.OpenMerge.VerificationInput.BaseSelectors)
					if err != nil {
						return err
					}
					for _, ref := range pin.OpenMerge.VerificationInput.ExpectedRefs {
						selected, err := coverage.ContainsRef(ref, "refs/heads/"+pin.DefaultBranch)
						if err != nil {
							return err
						}
						if selected && !slices.Contains(liveBases, strings.TrimPrefix(ref, "refs/heads/")) {
							inputsCurrent = false
						}
					}
					for _, base := range liveBases {
						selected, err := coverage.ContainsRef("refs/heads/"+base, "refs/heads/"+pin.DefaultBranch)
						if err != nil {
							return err
						}
						if selected {
							expected[base] = true
							if !slices.Contains(pin.OpenMerge.VerificationInput.ExpectedRefs, "refs/heads/"+base) {
								inputsCurrent = false
							}
						}
					}
				}
				switch {
				case q.Installation != request.Installation:
					q.Reason = "installation_inventory_unavailable"
				case q.Lane != "":
					q.Reason = "unsupported_lane"
				case q.ExpectedDigest == "" || pin.EffectiveDigest != q.ExpectedDigest || state.ConfigDigest != q.ExpectedDigest:
					q.Reason = "digest_mismatch"
				case !inputsCurrent:
					q.Reason = "discovery_inputs_stale"
				case !queue.AuthorityDraftAdopted(*pin):
					q.Reason = "adoption_missing"
				case state.WriteRestrictionStatus().State != "verified":
					q.Reason = "verification_incomplete"
				default:
					q.Reason = "complete"
				}
			}
			result.Queues = append(result.Queues, q)
		}
		for base := range expected {
			found := false
			for _, q := range partitions {
				if q.Base == base && q.Lane == "" {
					found = true
				}
			}
			if !found {
				result.Queues = append(result.Queues, AuthorityQueueCompletion{Installation: request.Installation, Base: base, Reason: "registration_missing"})
			}
		}
		return nil
	})
	if err != nil {
		return AuthorityCompletion{}, err
	}
	sort.Slice(result.Queues, func(i, j int) bool {
		a, b := result.Queues[i], result.Queues[j]
		if a.Base != b.Base {
			return strings.Compare(a.Base, b.Base) < 0
		}
		return a.ID < b.ID
	})
	result.Complete = len(result.Queues) > 0
	for _, q := range result.Queues {
		result.Complete = result.Complete && q.Reason == "complete"
	}
	return result, nil
}
