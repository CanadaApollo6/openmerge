package store

import (
 "errors"
 "slices"
 "testing"
 "time"

 "github.com/CanadaApollo6/openmerge/internal/dependency"
)

func TestDependencyDiagnosticRealEventDowngrade(t *testing.T) {
 f:=newDepFixture(t)
 f.observe(t,"waiting",f.a.base)
 e:=dependency.DiagnosticRecorded{Namespace:f.b.Namespace,ObservationEventID:f.observedID,Source:f.a.alias,Evidence:dependency.ReadEvidence{Stage:dependency.WorkRead,Declaration:f.observed.Declaration,Grant:dependency.Grant{SubjectID:"reader",ReturnedID:"reader",RepositoryID:f.a.alias.RepositoryID,ReadID:"source-read",Fresh:true,Allowed:true},Observation:f.observed,Code:"dependency-waiting"}}
 depSide(t,f.s,f.c.id,f.b,"diagnostic",e)
 kind:="dependency_diagnostic_recorded"
 if !slices.Contains(authorityDowngradeEventTypes,kind){t.Fatal("new kind missing from downgrade gate")}
 // Leave the real event alone, preventing another new kind or cursor from
 // masking a missing downgrade refusal for this event.
 _,err:=f.s.pool.Exec(t.Context(),`DELETE FROM observations`);must(t,err)
 _,err=f.s.pool.Exec(t.Context(),`DELETE FROM events WHERE type<>$1`,kind);must(t,err)
 reader:=&JournalReader{store:f.s,timeout:time.Minute}
 if err:=reader.CheckAuthorityDowngrade(t.Context());!errors.Is(err,ErrAuthorityDowngradeRefused){t.Fatal("real diagnostic permitted downgrade",err)}
}
