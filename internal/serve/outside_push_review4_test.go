package serve

import "testing"

func TestReview4RealRollbackToLandedTip(t *testing.T) {
	for _, method := range []string{"merge", "fast-forward"} {
		t.Run(method, func(t *testing.T) {
			s, w, f, before, req := outsidePushRound2Attempt(t, method, false)
			_, err := f.Land(t.Context(), repo, req)
			must(t, err)
			held, err := s.settle(t.Context(), w)
			must(t, err)
			if held {
				t.Fatal("not settled")
			}
			runOutsidePushAudit(t, s, w)
			c, err := f.PushBase(repo, "main", "real outside advance", nil)
			must(t, err)
			must(t, s.configuration(t.Context(), w))
			runOutsidePushAudit(t, s, w)
			l := before.Active.LandingCommit()
			must(t, f.SetRef(repo, "main", l))
			must(t, s.configuration(t.Context(), w))
			runOutsidePushAudit(t, s, w)
			state, _, err := s.store.Load(t.Context(), w.id)
			must(t, err)
			pushes := state.RecentOutsidePushes()
			if len(pushes) != 2 || pushes[1].PreviousSHA != c || pushes[1].NewSHA != l || !pushes[1].Rewrite {
				t.Fatalf("real C -> L rollback missing: records=%+v", pushes)
			}
		})
	}
}
