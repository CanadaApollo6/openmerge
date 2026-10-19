package serve

import (
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestStageAUnicodeApprovalIdentityRejected(t *testing.T) {
	for _, login := range []string{"Kam", "Kontributor", "Élodie", "élodie"} {
		t.Run(login, func(t *testing.T) {
			s, w, f := shadowFixture(t, 1, "main", "7", true)
			actor := forge.UserIdentity{ID: 21, Login: login, Kind: forge.UserAccount}
			// Even a stable ID and an affirmative forge grant cannot admit a
			// non-ASCII login into approval receipts or stances.
			f.SetIdentity(actor)
			c := shadowComment(100, "@openmerge approve "+shadowDigest(t, s, w))
			c.Author = actor
			f.SetNativeReviews(repo, 1, shadowNative(f, w, c))
			err := s.commands(t.Context(), w, 1)
			if !errors.Is(err, forge.ErrIdentityUnavailable) {
				t.Fatalf("Unicode approval result: %v", err)
			}
			state := shadowState(t, s, w)
			r, _ := state.PRRevision(1)
			if len(state.ReceiptRecords(r.ChangeID)) != 0 {
				t.Fatal("Unicode actor acquired a receipt")
			}
		})
	}
}
