package status

import (
 "bytes"
 "context"
 "encoding/json"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/CanadaApollo6/openmerge/internal/dependency"
 "github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest"
 "github.com/CanadaApollo6/openmerge/internal/forge"
 "github.com/CanadaApollo6/openmerge/internal/queue"
 "github.com/CanadaApollo6/openmerge/internal/store"
)

// The nil embedded forge is a tripwire for every forge method: rendering may
// only call Journals. An accidental forge call panics instead of reaching I/O.
type dependencyRenderSource struct { forge.Driver; journal store.Journal }
func (s dependencyRenderSource) Journals(_ context.Context, _ []store.QueueKey)([]store.Journal,error) { return []store.Journal{s.journal},nil }

func TestDependencyPrivacyGoldensNoRenderingFetch(t *testing.T) {
 states:=[]string{"missing","dependency-unresolved","dependency-waiting","dependency-closed","dependency-stale","dependency-landed","dependency-unavailable","dependency-out-of-scope","dependency-source-retargeted","dependency-not-on-base","dependency-unverified"}
 var wantAPI,wantCheck []byte
 for i,state:=range states {
  t.Run(state,func(t *testing.T){
   j,binding,ref,grant:=dependencytest.StatusJournal(t,state,i+1)
   denied:=grant;denied.Allowed=false
   mismatch:=grant;mismatch.ReturnedID="different"
   stale:=grant;stale.Fresh=false
   for _,audience:=range [][]dependency.Grant{nil,{denied},{mismatch},{stale},{grant,denied}} {
    projected,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:audience},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
    h:=Handler(dependencyRenderSource{journal:projected},[]store.QueueKey{j.Key},"",nil)
    w:=httptest.NewRecorder();h.ServeHTTP(w,httptest.NewRequest("GET","/v1/queues/acme/client/main/prs/2?events=1",nil))
    if w.Code!=200{t.Fatal(w.Code,w.Body)}
    q,e,_:=Project(projected,2,1,nil);if e==nil{t.Fatal("missing PR")}
    fullCheck:=DependencyCheckStatus(&q,e,forge.Status{State:forge.Testing,Reason:state,Summary:state})
    fullCheck.Summary+="\n\nCurrent state: "+WaitingCheckText(&q,*e,nil)+LedgerSummary(e)
    check,err:=json.Marshal(fullCheck);if err!=nil{t.Fatal(err)}
    if wantAPI==nil { wantAPI=bytes.Clone(w.Body.Bytes());wantCheck=bytes.Clone(check) }
    if !bytes.Equal(wantAPI,w.Body.Bytes())||!bytes.Equal(wantCheck,check){t.Fatalf("private source state leaked:\n%s\n%s",w.Body,check)}
    if e.WaitingReason.Code!=WaitingDependencies||e.WaitingReason.Text!="Dependency cannot be verified"||len(e.Dependency.Details)!=0||e.Dependency.URL!=""{t.Fatal(e.WaitingReason)}
   }
  })
 }
 reportingGolden(t,"dependency-private-api.json",wantAPI)
 reportingGolden(t,"dependency-private-check.json",wantCheck)
}

func TestDependencyAuthorizedDiagnosticsAndPrecedence(t *testing.T) {
 for _,code:=range []string{"dependency-waiting","dependency-closed","dependency-stale","dependency-landed","dependency-unavailable","dependency-source-retargeted"} {
  j,binding,ref,grant:=dependencytest.StatusJournal(t,code,1)
  j,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{grant}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
  q,e,_:=Project(j,2,20,nil)
  raw,_:=json.Marshal(e.Dependency)
  if !bytes.Contains(raw,[]byte(code)) { t.Fatalf("missing typed diagnosis %s: %s",code,raw) }
  if code=="dependency-landed" && e.WaitingReason.Code==WaitingDependencies { t.Fatal("verified dependencies still block") }
  for _,tc:=range []struct{status,gate string;code WaitingCode}{
   {"merged","",WaitingDone},{"closed","",WaitingDone},{"landing","",WaitingLanding},{"queued","landing-settlement",WaitingLanding},{"dequeued","",WaitingDequeued},{"paused","",WaitingPaused},{"waiting-entry","",WaitingEntry},{"blocked","",WaitingEntry},
  } {
   x:=*e;x.Status=tc.status;x.Gate=tc.gate
   if got:=DeriveWaitingReason(&q,x);got.Code!=tc.code {t.Fatalf("%s precedence: %+v",tc.status,got)}
  }
  q.Paused=true
  if got:=DeriveWaitingReason(&q,*e);got.Code!=WaitingPaused {t.Fatal(got)}
 }
}

func TestDependencyMissingDiagnosticAndBindingFailClosed(t *testing.T) {
 j,binding,ref,grant:=dependencytest.StatusJournal(t,"dependency-closed",1)
 for i:=range j.Events {if bytes.Contains(j.Events[i].Payload,[]byte(`"type":"dependency_diagnostic_recorded"`)){j.Events=append(j.Events[:i:i],j.Events[i+1:]...);break}}
 j,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{grant}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
 _,e,_:=Project(j,2,20,nil);if e.Dependency.Text!="Dependency cannot be verified" {t.Fatal(e.Dependency)}
 binding.Incarnation=strings.Repeat("f",64)
 _,err=ProjectDependencies(j,binding,nil,nil)
 if err==nil||err.Error()!="dependency status unavailable" {t.Fatal("identity failure leaked or accepted",err)}
}

func TestDependencySourceGrantConfusionFailsClosed(t *testing.T) {
 j,binding,ref,_:=dependencytest.StatusJournal(t,"dependency-closed",1)
 var unrelated dependency.Grant
 for i,record:=range j.Events {
  if !bytes.Contains(record.Payload,[]byte(`"type":"dependency_diagnostic_recorded"`)){continue}
  decoded,err:=dependency.DecodeEvent(record.Payload);if err!=nil{t.Fatal(err)}
  d:=decoded.(dependency.DiagnosticRecorded)
  d.Source.RepositoryID="unrelated-public-repo"
  d.Evidence.Grant.RepositoryID=d.Source.RepositoryID
  unrelated=d.Evidence.Grant
  _,j.Events[i].Payload,err=dependency.EncodeEvent(d);if err!=nil{t.Fatal(err)}
 }
 projected,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{unrelated}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
 _,e,_:=Project(projected,2,1,nil)
 if e.Dependency.Text!="Dependency cannot be verified"||len(e.Dependency.Diagnostics)!=0||!projected.DependencyRedacted{t.Fatal("unrelated repository authorized private source",e.Dependency)}
}

func TestDependencyHistoricalRevisionWithholdsCurrentDetails(t *testing.T) {
 j,binding,ref,grant:=dependencytest.StatusJournal(t,"dependency-closed",1)
 j,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{grant}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
 event:=queue.PRObserved{PR:2,HeadSHA:strings.Repeat("d",40),Eligible:true,Trusted:true}
 _,raw,err:=queue.EncodeEvent(event);if err!=nil{t.Fatal(err)}
 j.Events=append(j.Events,store.JournalEvent{ID:int64(len(j.Events)+1),Payload:raw,Event:event})
 id,err:=j.Key.ChangeID(2);if err!=nil{t.Fatal(err)}
 historical,_,found:=selectChangeRevision(j,string(id),1);if !found{t.Fatal("missing revision 1")}
 _,entry,_:=Project(historical,2,1,nil)
 if entry.Dependency!=nil||len(historical.DependencyStatus)!=0||len(j.DependencyStatus)==0{t.Fatal("current diagnostics contaminated historical revision")}
 // The current head cannot inherit an older declaration's diagnosis either.
 current,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{grant}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
 _,entry,_=Project(current,2,1,nil)
 if entry.Dependency!=nil{t.Fatal("old declaration described a new head")}
}

func TestDependencyAuthorizedReferencesRemainOriginal(t *testing.T) {
 j,binding,ref,grant:=dependencytest.StatusJournal(t,"dependency-closed",3)
 projected,err:=ProjectDependencies(j,binding,map[string][]dependency.Grant{ref:{grant}},dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
 q,_,why:=Project(projected,2,1,nil)
 last:=j.Events[len(j.Events)-1].ID
 if q.Watermark!=last||len(why.Events)!=1||why.Events[0].ID!=last{t.Fatalf("original references lost: watermark=%d why=%+v expected=%d",q.Watermark,why,last)}
}

func TestDependencyUnstartedProgressPrivacy(t *testing.T) {
 var wantAPI,wantCheck []byte
 for _,phase:=range []string{"queued","building","testing","discarded"} {
  j,b,_,_:=dependencytest.StatusJournal(t,"dependency-landed",1)
  appendEvent:=func(e queue.Event){
   _,raw,err:=queue.EncodeEvent(e);if err!=nil{t.Fatal(err)}
   j.Events=append(j.Events,store.JournalEvent{ID:int64(len(j.Events)+1),Payload:raw,Event:e})
  }
  at:=j.Events[0].RecordedAt
  if phase!="queued" {appendEvent(queue.BatchStarted{BatchID:"batch",BaseSHA:j.Seed.BaseSHA,PRs:[]queue.BatchPR{{PR:2,HeadSHA:strings.Repeat("b",40)}},At:at})}
  if phase=="testing"||phase=="discarded" {appendEvent(queue.TestRefBuilt{BatchID:"batch",TestedSHA:strings.Repeat("e",40),Method:queue.Merge,At:at})}
  if phase=="discarded" {
   appendEvent(queue.BatchesInvalidated{BatchIDs:[]string{"batch"},Reason:"dependency-changed"})
   appendEvent(queue.CheckCancellationRequested{BatchID:"batch",TestedSHA:strings.Repeat("e",40),Reason:"dependency-changed",TestRef:"refs/heads/test",Runs:[]queue.CheckRun{{ID:1,Name:"ci",Source:"app",State:"pending"}}})
  }
  j,err:=ProjectDependencies(j,b,nil,dependencytest.StatusSources());if err!=nil{t.Fatal(err)}
  // The new exact-discard kind must also be hidden before row selection.
  if phase=="discarded" {j.Events=append(j.Events,store.JournalEvent{ID:int64(len(j.Events)+1),Event:queue.DependencyCompositionsInvalidated{BatchIDs:[]string{"batch"},CompositionIDs:[]uint64{1},CauseEventIDs:[]uint64{2},Reason:"dependency-changed"}})}
  q,e,why:=Project(j,2,100,nil)
  ApplyPolicy(&q,e,queue.Config{MergeMethod:queue.Merge},at)
  response:=Response{Version:Version,Queue:&q,PR:e,Why:why}
  raw,err:=json.Marshal(response);if err!=nil{t.Fatal(err)}
  check:=DependencyCheckStatus(&q,e,forge.Status{State:forge.Testing,Reason:phase,Summary:phase})
  check.Summary+="\n\nCurrent state: "+WaitingCheckText(&q,*e,nil)+LedgerSummary(e)
  checkRaw,err:=json.Marshal(check);if err!=nil{t.Fatal(err)}
  if wantAPI==nil {wantAPI=raw;wantCheck=checkRaw}
  if !bytes.Equal(wantAPI,raw)||!bytes.Equal(wantCheck,checkRaw){t.Fatalf("%s exposed unstarted dependency progress:\n%s\n%s",phase,raw,checkRaw)}
 }
}
