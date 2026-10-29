package statusfixture

import (
 "fmt"
 "math/rand"
  "testing"

 "github.com/CanadaApollo6/openmerge/internal/dependency"
 "github.com/CanadaApollo6/openmerge/internal/queue"
 "github.com/CanadaApollo6/openmerge/internal/store"
)

// MetamorphicJournal pairs private histories with the same public observation.
// Seed chooses the public transition; world chooses private progress and batch history.
func MetamorphicJournal(t testing.TB, seed int64, depth, world int) (store.Journal, dependency.JournalInitialized, []int) {
 t.Helper()
 variants := []struct{ code, phase string }{
  {"dependency-waiting", "queued"}, {"dependency-closed", "queued"},
  {"dependency-stale", "queued"}, {"dependency-unavailable", "queued"},
  {"dependency-landed", "testing"}, {"dependency-waiting", "invalidating"},
  {"dependency-waiting", "discarded"},
 }
 v := variants[world%len(variants)]
 j, binding, _, _, prs := MultiJournal(t, v.code, v.phase, depth, true)
 appendEvent := func(e queue.Event) {
  t.Helper()
  _, raw, err := queue.EncodeEvent(e); if err != nil { t.Fatal(err) }
  j.Events = append(j.Events, store.JournalEvent{ID:int64(len(j.Events)+1), RecordedAt:j.Events[0].RecordedAt, Event:e, Payload:raw})
 }
 // Provenance is only observable when a batch exists; the public transition
 // occurs in both worlds after that observation.
 if v.phase == "testing" && seed%2 == 0 {
  appendEvent(queue.CheckProvenanceObserved{BatchID:"b1", TestedSHA:fmt.Sprintf("%040x", 100), Checks:[]queue.UnboundCheck{{Name:"ci", Source:"github-actions", HeadBranch:"other"}}})
 }
 rng := rand.New(rand.NewSource(seed))
 switch seed % 5 {
 case 0: appendEvent(queue.PRObserved{PR:3, HeadSHA:fmt.Sprintf("%040x",rng.Int63()), Eligible:true, Trusted:true})
 case 1: appendEvent(queue.PRClosed{PR:3})
 case 2: appendEvent(queue.DequeueRequested{PR:3})
 case 3: appendEvent(queue.PRObserved{PR:3, HeadSHA:fmt.Sprintf("%040x",13), Eligible:false, Trusted:true})
 case 4: appendEvent(queue.PauseRequested{Reason:"operator", Detail:"public maintenance"})
 }
 return j,binding,prs
}
