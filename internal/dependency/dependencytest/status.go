package dependencytest

import (
 "strings"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/dependency"
 "github.com/CanadaApollo6/openmerge/internal/queue"
 "github.com/CanadaApollo6/openmerge/internal/store"
)

// StatusJournal builds synthetic consumer history for every presentation surface.
func StatusJournal(t testing.TB, code string, repeats int) (store.Journal, dependency.JournalInitialized, string, dependency.Grant) {
 t.Helper()
 r := Request()
 var token [32]byte; token[0]=1
 binding,err := dependency.NewJournalIdentity("openmerge-db-v2:sha256:"+strings.Repeat("a",64),token,r.Registry.Scope.ForgeInstance,r.Registry.Scope.Installation)
 if err != nil { t.Fatal(err) }
 j := store.Journal{Key:store.QueueKey{Installation:"7",Repository:"acme/client",BaseBranch:"main"},Seed:queue.State{BaseBranch:"main",BaseSHA:strings.Repeat("0",40)}}
 at := time.Date(2026,1,1,0,0,0,0,time.UTC)
 appendRecord := func(event any) uint64 {
  t.Helper()
  var raw []byte; var qe queue.Event; var err error
  switch e := event.(type) {
  case queue.Event: _,raw,err=queue.EncodeEvent(e);qe=e
  case dependency.Event: _,raw,err=dependency.EncodeEvent(e)
  default: t.Fatalf("unsupported %T",event)
  }
  if err != nil { t.Fatal(err) }
  id:=uint64(len(j.Events)+1)
  j.Events=append(j.Events,store.JournalEvent{ID:int64(id),RecordedAt:at,Payload:raw,Event:qe})
  return id
 }
 appendRecord(queue.PRObserved{PR:2,HeadSHA:strings.Repeat("b",40),Eligible:true,Trusted:true})
 appendRecord(queue.EnqueueRequested{PR:2,At:at})
 appendRecord(binding)
 p:=dependency.Policy{Mode:"ordered",AllowFrom:[]string{r.Edge.Queue.Repository},AllowTo:[]string{}}
 appendRecord(dependency.PolicyApplied{Enabled:true,Namespace:binding.Namespace,Alias:r.Consumer.Alias,Queue:r.Consumer.Queue,DefaultBranch:"main",ReadRef:"refs/heads/main",ReadID:"policy",Commit:strings.Repeat("c",40),Digest:dependency.PolicyDigest(p,true),Policy:p,Writers:[]dependency.Grant{}})
 changeID,err := j.Key.ChangeID(2);if err!=nil {t.Fatal(err)}
 r.Consumer.ChangeID=string(changeID)
 declarationID:=appendRecord(r.Consumer)
 grant:=dependency.Grant{SubjectID:"reader",ReturnedID:"reader",RepositoryID:r.Source.Alias.RepositoryID,ReadID:"recorded-audience",Fresh:true,Allowed:true}
 var prior uint64
 if code!="missing" {
  result:="unavailable"
  switch code {case "dependency-waiting":result="waiting";case "dependency-stale":result="stale";case "dependency-landed":result="landed"}
  for n:=0;n<repeats;n++ {
   o:=dependency.Observed{Namespace:binding.Namespace,Alias:r.Consumer.Alias,ChangeID:r.Consumer.ChangeID,Revision:1,Declaration:dependency.Basis{EventID:declarationID,Digest:r.Consumer.Digest},Source:r.Edge.Reference,ExpectedPrior:prior,ReadSequence:uint64(n+1),SourceRevision:r.Source.Current,SourceBase:r.Source.BaseSHA,Result:result}
   prior=appendRecord(o)
   appendRecord(queue.DependencyWaitsWithheld{PRs:[]queue.BatchPR{{PR:2,HeadSHA:strings.Repeat("b",40)}}})
   appendRecord(dependency.DiagnosticRecorded{Namespace:binding.Namespace,ObservationEventID:prior,Source:r.Source.Alias,Evidence:dependency.ReadEvidence{Stage:dependency.WorkRead,Declaration:o.Declaration,Grant:grant,Observation:o,Code:code}})
  }
 }
 // A later public row exposes an ID gap unless the read projection hides it.
 appendRecord(queue.RefreshRequested{})
 return j,binding,r.Edge.Reference,grant
}


// StatusSources is the independently recorded source-journal binding snapshot.
func StatusSources() map[string]dependency.SourceRecord {
 r:=Request()
 return map[string]dependency.SourceRecord{r.Source.Reference.ChangeID:r.Source}
}
