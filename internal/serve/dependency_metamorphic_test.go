package serve

import (
 "bytes"
 "encoding/json"
 "fmt"
 "strings"
 "testing"

 "github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
 "github.com/CanadaApollo6/openmerge/internal/forge"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)

// Exercise the real report and durable deduplication, including the interval
// after observation but before the planner executes a pending dequeue.
func TestDependencyPendingPublicationPrivacy(t *testing.T) {
 for _,transition:=range []string{"head","closed","requested","retargeted","paused"} {
  t.Run(transition,func(t *testing.T){
   var baseline []byte
   baselineWrites := -1
   for _,phase:=range []string{"queued","testing"} {
    t.Run(phase,func(t *testing.T){
     db,conn:=databaseConnection(t);s,w,f,_:=fixtureWithStore(t,repositoryConfig,db)
     code:="dependency-waiting";if phase=="testing"{code="dependency-landed"}
     j,b,_,_,_:=statusfixture.MultiJournal(t,code,phase,1,true)
     id,err:=db.EnsureQueue(t.Context(),j.Key);must(t,err);w.id=id;w.Key=j.Key
     r:=repo;r.Path=j.Key.Repository;f.Init(r,"main","fence")
     name,raw,err:=queue.EncodeEvent(queue.BaseMoved{SHA:j.Seed.BaseSHA});must(t,err)
     _,err=conn.Exec(t.Context(),`INSERT INTO events(id,queue_id,type,payload) VALUES(100,$1,$2,$3::jsonb)`,id,name,string(raw));must(t,err)
     for _,e:=range j.Events {
      var data any;must(t,json.Unmarshal(e.Payload,&data));shiftDependencyFixtureRefs(data);raw,err=json.Marshal(data);must(t,err)
      var envelope struct{Type string `json:"type"`};must(t,json.Unmarshal(raw,&envelope))
      _,err=conn.Exec(t.Context(),`INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`,e.ID+100,id,envelope.Type,string(raw));must(t,err)
     }
     next:=int64(len(j.Events)+101)
     insert:=func(event queue.Event){t.Helper(); name,raw,err:=queue.EncodeEvent(event);must(t,err);_,err=conn.Exec(t.Context(),`INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`,next,id,name,string(raw));must(t,err);next++}
     if phase=="testing" {insert(queue.CheckProvenanceObserved{BatchID:"b1",TestedSHA:fmt.Sprintf("%040x",100),Checks:[]queue.UnboundCheck{{Name:"ci",Source:"github-actions",HeadBranch:"other"}}})}
     switch transition {
     case "head":insert(queue.PRObserved{PR:3,HeadSHA:strings.Repeat("9",40),Eligible:true,Trusted:true})
     case "closed":insert(queue.PRClosed{PR:3})
     case "requested":insert(queue.DequeueRequested{PR:3})
     case "retargeted":insert(queue.PRObserved{PR:3,HeadSHA:fmt.Sprintf("%040x",13),Eligible:false,Trusted:true})
     case "paused":insert(queue.PauseRequested{Reason:"operator",Detail:"public maintenance"})
     }
     w.dependencies=&dependencyPass{binding:b,sources:statusfixture.StatusSources()};w.prs=map[int]forge.PullRequest{}
     state,_,err:=db.Load(t.Context(),id);must(t,err);for _,e:=range state.Entries {w.prs[e.PR]=forge.PullRequest{Number:e.PR,HeadSHA:e.HeadSHA}}
     driver:=&instructionDriver{Driver:w.Driver};w.Driver=driver
     must(t,s.report(t.Context(),w));got,err:=db.Observation(t.Context(),id,"status/3");must(t,err)
     if len(got)==0 {t.Fatal("no actual publication")}
     if baseline==nil {baseline=bytes.Clone(got);baselineWrites=driver.writes} else if !bytes.Equal(baseline,got)||baselineWrites!=driver.writes {t.Fatalf("publication differs: writes %d/%d\n%s\n%s",baselineWrites,driver.writes,baseline,got)}
     writes:=driver.writes;must(t,s.report(t.Context(),w));if driver.writes!=writes {t.Fatalf("unchanged report published %d extra checks",driver.writes-writes)}
    })
   }
  })
 }
}

func shiftDependencyFixtureRefs(value any) {
 switch v:=value.(type) {
 case map[string]any:
  for k,x:=range v {if (k=="observation_ids"||k=="composition_ids"||k=="cause_event_ids") {for i,n:=range x.([]any){x.([]any)[i]=n.(float64)+100};continue};if n,ok:=x.(float64);ok&&n>0&&(strings.HasSuffix(k,"event_id")||k=="expected_prior"){v[k]=n+100}else{shiftDependencyFixtureRefs(x)}}
 case []any:for _,x:=range v{shiftDependencyFixtureRefs(x)}
 }
}

func TestDependencyMetamorphicPublication(t *testing.T) {
 seeds:=5;if !testing.Short(){seeds=40}
 for _,depth:=range []int{1,4} {for seed:=0;seed<seeds;seed++ {t.Run(fmt.Sprintf("depth-%d/seed-%d",depth,seed),func(t *testing.T){
  var baseline map[int][]byte;baselineWrites:=-1
  for world:=0;world<7;world++ {
   j,b,prs:=statusfixture.MetamorphicJournal(t,int64(seed),depth,world)
   db,conn:=databaseConnection(t);s,w,f,_:=fixtureWithStore(t,repositoryConfig,db)
   id,err:=db.EnsureQueue(t.Context(),j.Key);must(t,err);w.id=id;w.Key=j.Key
   r:=repo;r.Path=j.Key.Repository;f.Init(r,"main","fence")
   name,raw,err:=queue.EncodeEvent(queue.BaseMoved{SHA:j.Seed.BaseSHA});must(t,err)
   _,err=conn.Exec(t.Context(),`INSERT INTO events(id,queue_id,type,payload) VALUES(100,$1,$2,$3::jsonb)`,id,name,string(raw));must(t,err)
   for _,e:=range j.Events {
    var data any;must(t,json.Unmarshal(e.Payload,&data));shiftDependencyFixtureRefs(data);raw,err=json.Marshal(data);must(t,err)
    var envelope struct{Type string `json:"type"`};must(t,json.Unmarshal(raw,&envelope))
    _,err=conn.Exec(t.Context(),`INSERT INTO events(id,queue_id,type,payload) VALUES($1,$2,$3,$4::jsonb)`,e.ID+100,id,envelope.Type,string(raw));must(t,err)
   }
   w.dependencies=&dependencyPass{binding:b,sources:statusfixture.StatusSources()};w.prs=map[int]forge.PullRequest{}
   state,_,err:=db.Load(t.Context(),id);must(t,err);for _,e:=range state.Entries {w.prs[e.PR]=forge.PullRequest{Number:e.PR,HeadSHA:e.HeadSHA}}
   driver:=&instructionDriver{Driver:w.Driver};w.Driver=driver;must(t,s.report(t.Context(),w))
   got:=map[int][]byte{};for _,pr:=range prs {got[pr],err=db.Observation(t.Context(),id,fmt.Sprintf("status/%d",pr));must(t,err)}
   if baseline==nil {baseline=got;baselineWrites=driver.writes} else {
    if baselineWrites!=driver.writes {t.Fatalf("world %d wrote %d checks, want %d",world,driver.writes,baselineWrites)}
    for _,pr:=range prs {if !bytes.Equal(baseline[pr],got[pr]) {t.Fatalf("world %d PR %d publication differs:\n%s\n%s",world,pr,baseline[pr],got[pr])}}
   }
   writes:=driver.writes;must(t,s.report(t.Context(),w));if driver.writes!=writes {t.Fatalf("world %d republished %d checks",world,driver.writes-writes)}
  }
 })}}
}
