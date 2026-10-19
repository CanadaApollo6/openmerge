package serve

import (
	"context"
	"fmt"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
	"github.com/CanadaApollo6/openmerge/internal/queue"
)

// handleLegacyCommand applies the legacy command protocol to one comment.
// Both the legacy comment reader and Stage A's native review reader use this
// function so quota, waiting-queue handling, authorization, fresh reads,
// operator clears, receipts, and queued replies cannot drift apart.
func (s *Server) handleLegacyCommand(ctx context.Context, w *worker, number int, c forge.Comment) (bool, error) {
	if err := s.quota(); err != nil {
		return false, err
	}
	receipt := fmt.Sprintf("comment/%d", c.ID)
	seen, err := s.store.Observation(ctx, w.id, receipt)
	if err != nil {
		return false, err
	}
	if len(seen) == 0 {
		var events []queue.Event
		var outcome any = true
		if cmd := command(c.Body); cmd != "" && c.Author != w.BotLogin {
			fields := strings.Fields(cmd)
			if fields[0] == "queue" && w.managed && !w.ready {
				return true, s.ignoreWaitingCommand(ctx, w, number, c)
			}
			allowed, err := w.Driver.CanWrite(ctx, w.repo(), c.Author)
			if err != nil {
				return false, err
			}
			if !allowed {
				if err := w.Driver.Comment(ctx, w.repo(), number, receipt+"/unauthorized", "Only repository writers can use @openmerge commands."); err != nil {
					return false, err
				}
			} else {
				switch fields[0] {
				case "queue":
					// Trust must be freshly read even if UpdatedAt did not change.
					pr, err := s.observe(ctx, w, number)
					if err != nil {
						return false, err
					}
					if len(fields) == 2 && !strings.EqualFold(pr.HeadSHA, fields[1]) {
						if err := w.Driver.Comment(ctx, w.repo(), number, receipt+"/head-changed", "The SHA does not match the current head "+pr.HeadSHA+". Please re-issue @openmerge queue with the intended SHA."); err != nil {
							return false, err
						}
					} else {
						events = append(events, queue.EnqueueRequested{PR: number, At: c.CreatedAt})
						outcome = queueReceipt{QueuedSHA: pr.HeadSHA}
					}
				case "dequeue":
					events = append(events, queue.DequeueRequested{PR: number})
				case "refresh":
					if _, err := s.observe(ctx, w, number); err != nil {
						return false, err
					}
					clears, err := s.operatorClears(ctx, w)
					if err != nil {
						return false, err
					}
					events = append(events, clears...)
					events = append(events, queue.RefreshRequested{})
				}
			}
		}
		if err := s.record(ctx, w, receipt, outcome, events...); err != nil {
			return false, err
		}
	}
	return false, s.replyQueued(ctx, w, number, receipt)
}
