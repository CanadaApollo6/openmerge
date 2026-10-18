package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// approvalCommand extends the whole-comment grammar only for candidate queues.
// Even malformed digests reach PrepareBareReceipt and become bounded rejections.
func approvalCommand(body string) (verb, digest string) {
	body = strings.Trim(strings.ReplaceAll(body, "\r\n", "\n"), " \t\r\n")
	if body == "@openmerge unapprove" || body == "@openmerge\tunapprove" {
		return "unapprove", ""
	}
	for _, prefix := range []string{"@openmerge approve", "@openmerge\tapprove"} {
		if body == prefix {
			return "approve", ""
		}
		if strings.HasPrefix(body, prefix) && len(body) > len(prefix) && (body[len(prefix)] == ' ' || body[len(prefix)] == '\t') {
			raw := strings.TrimLeft(body[len(prefix):], " \t")
			// Multi-line prose or additional arguments are not whole-comment commands.
			if strings.ContainsAny(raw, " \t\r\n") {
				return "", ""
			}
			return "approve", raw
		}
	}
	return "", ""
}

type approvalCommandOutcome struct {
	Detail string `json:"approval_detail"`
}

func (s *Server) stageACommands(ctx context.Context, w *worker, number int) error {
	_, reader, err := w.stageReaders()
	if err != nil {
		return err
	}
	native, err := reader.NativeReviews(ctx, w.repo(), number)
	if err != nil {
		return err
	}
	return s.stageACommandsFromNative(ctx, w, number, native, false)
}

// stageACommandsFromNative folds one complete native review response into the
// command journal. approvalsOnly is used by final preflight: it may consume
// approval commands from this response, but it must leave legacy commands for
// the normal command pass and must not advance the shared cursor past them.
func (s *Server) stageACommandsFromNative(ctx context.Context, w *worker, number int, native forge.NativeReviews, approvalsOnly bool) error {
	ids, _, err := w.stageReaders()
	if err != nil {
		return err
	}
	raw, err := s.store.Observation(ctx, w.id, fmt.Sprintf("cursor/%d", number))
	if err != nil {
		return err
	}
	var since time.Time
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &since); err != nil {
			return err
		}
	}
	slices.SortStableFunc(native.Comments, func(a, b forge.NativeComment) int {
		if c := a.Created.Time.Compare(b.Created.Time); c != 0 {
			return c
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, c := range native.Comments {
		if c.Kind != forge.IssueComment || c.ID <= 0 || c.Created.Time.Before(since) {
			continue
		}
		verb, digest := approvalCommand(c.Body)
		legacy := command(c.Body)
		if approvalsOnly && verb == "" {
			continue
		}
		if !approvalsOnly && legacy != "" {
			stop, err := s.handleLegacyCommand(ctx, w, number, forge.Comment{ID: c.ID, Author: c.Author.Login, Body: c.Body, CreatedAt: c.Created.Time})
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			if c.Created.Time.After(since) {
				since = c.Created.Time
			}
			continue
		}
		if verb != "" {
			if err := s.quota(); err != nil {
				return err
			}
		}
		key := fmt.Sprintf("comment/%d", c.ID)
		seen, err := s.store.Observation(ctx, w.id, key)
		if err != nil {
			return err
		}
		if len(seen) == 0 {
			events := []queue.Event{}
			var outcome any = true
			if (verb != "" || legacy != "") && c.Author.Login != w.BotLogin {
				grant, err := ids.WriteGrant(ctx, w.repo(), c.Author)
				if err != nil {
					if !errors.Is(err, forge.ErrIdentityMismatch) && !errors.Is(err, forge.ErrIdentityUnavailable) {
						return err
					}
					outcome = approvalCommandOutcome{Detail: "Only an identity-bound repository writer can use @openmerge commands."}
				} else if !grant.CanWrite(c.Author.ID) {
					outcome = approvalCommandOutcome{Detail: "Only an identity-bound repository writer can use @openmerge commands."}
				} else {
					// Bind unapprove and bare digest to the authenticated processing revision,
					// after a fresh PR read. A positive digest is never rebound to old evidence.
					pr, err := s.observe(ctx, w, number)
					if err != nil {
						return err
					}
					state, _, err := s.store.Load(ctx, w.id)
					if err != nil {
						return err
					}
					r, _ := state.PRRevision(number)
					b := approvalBasis(r)
					actor, err := policyIdentity(c.Author)
					if err != nil {
						return err
					}
					if _, mapped := w.stageA.mappings[actor.ID]; mapped {
						actor.Kind = cp.AgentAccount
					}
					// Authentication and binding acceptance are independent of current
					// membership. In particular, retaining an unapprove prevents an older
					// native approval from reviving if this writer rejoins a team later.
					switch verb {
					case "approve":
						receipt, err := state.PrepareBareReceipt(w.stageA.policy, b, actor, strconv.FormatInt(c.ID, 10), c.Created.Time, digest)
						if err != nil {
							return err
						}
						if err := state.CheckReceiptAdmission(receipt); err != nil {
							var limit queue.ReceiptRejectionLimitError
							if errors.As(err, &limit) {
								outcome = approvalCommandOutcome{Detail: limit.Error()}
								break
							}
							return err
						}
						events = append(events, receipt)
						detail := "Approval receipt accepted for digest " + receipt.Current.BriefDigest
						if !receipt.Accepted {
							detail = fmt.Sprintf("Approval rejected: %s; supplied %q; current %s (revision %d, head %s). Re-read the current brief and post a new command.", receipt.ReasonCode, receipt.Supplied.BriefDigest, receipt.Current.BriefDigest, b.Revision, b.Head)
						}
						outcome = approvalCommandOutcome{Detail: detail}
					case "unapprove":
						e := queue.ReviewStanceObserved{Basis: b, Reviewer: actor, Stance: cp.Stance{UserID: actor.ID, Kind: cp.Withdraw, Revision: uint64(b.Revision), Head: b.Head, Source: cp.StanceSource{Channel: cp.CommentCommand, ID: strconv.FormatInt(c.ID, 10), Sequence: uint64(c.ID), ActingUserID: actor.ID, Time: c.Created.Time, TimeKnown: true, Precision: cp.ExactTime, UpperTimeBound: c.Created.Time.Add(time.Second)}}, SourceKind: forge.CommentCreationTime, SourceTime: forge.NativeTime{Source: forge.CommentCreationTime, Time: c.Created.Time, Precision: time.Second, UpperBound: c.Created.Time.Add(time.Second)}}
						events = append(events, e)
						outcome = approvalCommandOutcome{Detail: fmt.Sprintf("Approval withdrawn for revision %d, head %s.", b.Revision, b.Head)}
					default:
						fields := strings.Fields(legacy)
						switch fields[0] {
						case "queue":
							if len(fields) == 2 && !strings.EqualFold(fields[1], pr.HeadSHA) {
								outcome = approvalCommandOutcome{Detail: "The SHA does not match the current head " + pr.HeadSHA}
							} else {
								events = append(events, queue.EnqueueRequested{PR: number, At: c.Created.Time})
								outcome = queueReceipt{QueuedSHA: pr.HeadSHA}
							}
						case "dequeue":
							events = append(events, queue.DequeueRequested{PR: number})
						case "refresh":
							events = append(events, queue.RefreshRequested{})
						}
					}
				}
			}
			data, err := json.Marshal(outcome)
			if err != nil {
				return err
			}
			policy := w.stageA.policy
			configDigest := w.configDigest
			err = s.store.RecordEvidence(ctx, w.id, key, data, true, func(state queue.State) error {
				if state.ConfigDigest != configDigest || state.SourceBlocked(queue.ConfigSource) {
					return fmt.Errorf("command configuration changed")
				}
				for _, event := range events {
					if e, ok := event.(queue.ApprovalReceiptRecorded); ok {
						current, err := state.PrepareBareReceipt(policy, e.Basis, e.Actor, e.CommentID, e.CreatedAt, e.Supplied.BriefDigest)
						if err != nil {
							return err
						}
						if current != e {
							return fmt.Errorf("receipt evidence changed before acceptance")
						}
					}
					switch event.(type) {
					case queue.EnqueueRequested, queue.DequeueRequested, queue.RefreshRequested:
						continue
					}
					if err := preflightStageA(state, []queue.Event{event}); err != nil {
						return err
					}
					state = queue.Apply(state, event)
				}
				return nil
			}, events...)
			if err != nil {
				return err
			}
		}
		// Read the immutable outcome after commit so a crash before reply repairs
		// itself without accepting again or using edited comment content.
		raw, err := s.store.Observation(ctx, w.id, key)
		if err != nil {
			return err
		}
		if len(raw) > 0 && raw[0] == '{' {
			var outcome approvalCommandOutcome
			if err := json.Unmarshal(raw, &outcome); err != nil {
				return err
			}
			if outcome.Detail != "" {
				if err := w.Driver.Comment(ctx, w.repo(), number, key+"/approval", outcome.Detail); err != nil {
					return err
				}
			}
		}
		if err := s.replyQueued(ctx, w, number, key); err != nil {
			return err
		}
		if c.Created.Time.After(since) {
			since = c.Created.Time
		}
	}
	if approvalsOnly {
		// Leave the shared cursor untouched. A final gate read may have skipped
		// an older legacy command; the normal command pass must still see it.
		return nil
	}
	return s.record(ctx, w, fmt.Sprintf("cursor/%d", number), since)
}
