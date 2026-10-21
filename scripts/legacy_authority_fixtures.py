"""Shared deterministic DB fixtures for the legacy HTTP/CLI differential probes."""
GO_IMPORTS = '''
 "context"
 "fmt"
 "net/url"
 "os"
 "testing"
 "time"
 "github.com/CanadaApollo6/openmerge/internal/queue"
 "github.com/CanadaApollo6/openmerge/internal/store"
 "github.com/jackc/pgx/v5"
'''
GO_FIXTURES = r'''
func legacyDBFixture(t *testing.T,kind string)(*store.Store,store.QueueKey) {
 t.Helper()
 raw:=os.Getenv("OPENMERGE_TEST_DATABASE_URL")
 if raw=="" {t.Fatal("OPENMERGE_TEST_DATABASE_URL must point to the test DB on localhost:54329")}
 u,err:=url.Parse(raw);if err!=nil {t.Fatal(err)}
 if u.Hostname()!="localhost" || u.Port()!="54329" {t.Fatal("legacy probe requires localhost:54329")}
 conn,err:=pgx.Connect(t.Context(),raw);if err!=nil {t.Fatal(err)}
 schema:=fmt.Sprintf("legacy_authority_%d",time.Now().UnixNano())
 _,err=conn.Exec(t.Context(),"CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize());if err!=nil {t.Fatal(err)}
 t.Cleanup(func(){_,err:=conn.Exec(context.Background(),"DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE");if err!=nil {t.Error(err)};_ = conn.Close(context.Background())})
 params:=u.Query();params.Set("search_path",schema);u.RawQuery=params.Encode()
 db,err:=store.Open(t.Context(),u.String(),store.WithTestLockScope(schema));if err!=nil {t.Fatal(err)}
 t.Cleanup(db.Close)
 key:=store.QueueKey{Installation:"7",Repository:"owner/repo",BaseBranch:"main"}
 if kind=="empty" {return db,key}
 id,err:=db.EnsureQueue(t.Context(),key);if err!=nil {t.Fatal(err)}
 if err:=db.InitializeFence(t.Context(),id,"fence");err!=nil {t.Fatal(err)}
 at:=time.Date(2026,10,16,12,0,0,0,time.UTC)
 events:=[]queue.Event{queue.BaseMoved{SHA:"base"}}
 switch kind {
 case "waiting":events=append(events,queue.PRObserved{PR:1,HeadSHA:"head",Eligible:true,Trusted:true},queue.EnqueueRequested{PR:1,At:at})
 case "paused":events=append(events,queue.PauseRequested{Reason:"operator",Detail:"fixture"})
 case "active":events=append(events,queue.PRObserved{PR:1,HeadSHA:"head",Eligible:true,Trusted:true},queue.EnqueueRequested{PR:1,At:at},queue.BatchStarted{BatchID:"b1",BaseSHA:"base",PRs:[]queue.BatchPR{{PR:1,HeadSHA:"head"}},At:at},queue.TestRefBuilt{BatchID:"b1",TestedSHA:"tested",At:at,Method:queue.Squash})
 case "outside":events=append(events,queue.OutsidePushRecorded{PreviousSHA:"base",NewSHA:"outside",AncestryKnown:true,HistoryKnown:true,Commits:[]string{"outside"},VerificationBeforeLanding:"none",At:at})
 }
 if _,err:=db.Append(t.Context(),id,events,"");err!=nil {t.Fatal(err)}
 _,err=conn.Exec(t.Context(),"UPDATE "+pgx.Identifier{schema,"events"}.Sanitize()+" SET recorded_at=$1",at);if err!=nil {t.Fatal(err)}
 return db,key
}
'''
