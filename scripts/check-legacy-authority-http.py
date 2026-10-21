#!/usr/bin/env python3
"""Compare complete deterministic legacy HTTP responses against a base checkout.

Run under the project's heavy-command flock with GOFLAGS/GOMEMLIMIT set.
Only isolated test-DB schemas are used; no forge, live API or network is used. No fields are stripped.
"""
import base64
import json
import hashlib
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from legacy_authority_fixtures import GO_IMPORTS, GO_FIXTURES

ROOT = Path(__file__).resolve().parents[1]
BASE = Path(sys.argv[1]).resolve()
PROBE = r'''package status
import (
 "bytes"
 "context"
 "encoding/base64"
 "fmt"
 "net/http/httptest"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/store"
)
type legacyHTTPSource struct { journals []store.Journal }
func (s legacyHTTPSource) Journals(context.Context,[]store.QueueKey) ([]store.Journal,error) { return s.journals,nil }
func TestLegacyAuthorityHTTPProbe(t *testing.T) {
 j:=journal(append(ledgerChain(),identified("b1",1,1,"success"))...)
 j.Key=store.QueueKey{Installation:"7",Repository:"owner/repo",BaseBranch:"main"}
 for _,token:=range []string{"","secret"} {
  h:=Handler(legacyHTTPSource{[]store.Journal{j}},[]store.QueueKey{j.Key},token,nil)
  for _,path:=range []string{"/v1/queues","/v1/queues/owner/repo","/v1/queues/owner/repo/main","/v1/queues/owner/repo/main/prs/1","/v1/queues/owner/repo/main/prs/1?events=1","/v1/queues/owner/repo/main?bad=1","/v1/missing"} {
   for _,method:=range []string{"GET","POST"} {
    for _,auth:=range []bool{false,true} {
     r:=httptest.NewRequest(method,path,nil)
     if auth { r.Header.Set("Authorization","Bearer secret") }
     rr:=httptest.NewRecorder();h.ServeHTTP(rr,r)
     var raw bytes.Buffer
     if err:=rr.Result().Write(&raw);err!=nil { t.Fatal(err) }
     fmt.Printf("LEGACY-HTTP %s\n",base64.StdEncoding.EncodeToString(raw.Bytes()))
    }
   }
  }
 }
}
'''
SERVER_PROBE = "package serve\nimport (\n" + GO_IMPORTS + r'''
 "bytes"
 "encoding/base64"
 "net/http/httptest"
 "github.com/CanadaApollo6/openmerge/internal/status"
)
''' + GO_FIXTURES + r'''
func TestLegacyAuthorityServerHTTPProbe(t *testing.T) {
 for _,kind:=range []string{"empty","idle","waiting","paused","active","outside"} {
  db,key:=legacyDBFixture(t,kind)
  s,err:=newServer(db,Options{});if err!=nil {t.Fatal(err)}
  w:=&worker{Target:Target{Key:key},managed:true,waiting:"validation pending"}
  w.cfg.Speculation.MaxDepth=4
  s.queues=[]*worker{w}
  id,err:=key.ChangeID(1);if err!=nil {t.Fatal(err)}
  change:="/v1/queues/owner/repo/main/changes/"+string(id)
  paths:=[]string{"/v1/queues","/v1/queues/owner/repo","/v1/queues/owner/repo/main","/v1/queues/owner/repo/main/prs/1","/v1/queues/owner/repo/main/prs/1?events=1","/v1/queues/owner/repo/main?bad=1","/v1/missing","/v1/discovery",change,change+"/decisions",change+"/brief?audience=reviewer",change+"/brief?audience=operator",change+"?revision=1"}
  for _,ready:=range []bool{false,true} {
   w.ready=ready
   for _,token:=range []string{"","secret"} {
    h:=status.DynamicHandler(s,s.Keys,token,nil,func()status.Discovery{return status.Discovery{}})
    for _,path:=range paths {
     for _,method:=range []string{"GET","POST"} {
      for _,auth:=range []bool{false,true} {
       r:=httptest.NewRequest(method,path,nil);if auth {r.Header.Set("Authorization","Bearer secret")}
       rr:=httptest.NewRecorder();h.ServeHTTP(rr,r)
       var raw bytes.Buffer
       if err:=rr.Result().Write(&raw);err!=nil {t.Fatal(err)}
       fmt.Printf("LEGACY-HTTP %s\n",base64.StdEncoding.EncodeToString(raw.Bytes()))
      }
     }
    }
   }
  }
 }
}
'''
with tempfile.TemporaryDirectory(prefix='authority-http-',dir=Path.home()/'.cache') as tmp:
    directory=Path(tmp)
    probe=directory/'probe.go'
    probe.write_text(PROBE)
    server_probe=directory/'server_probe.go'
    server_probe.write_text(SERVER_PROBE)
    def run(tree):
        traces=[]
        for package, filename, source, test in [('internal/status','legacy_authority_http_probe_test.go',probe,'TestLegacyAuthorityHTTPProbe'),('internal/serve','legacy_authority_server_http_probe_test.go',server_probe,'TestLegacyAuthorityServerHTTPProbe')]:
            overlay=directory/'overlay.json'
            overlay.write_text(json.dumps({'Replace':{str(tree/package/filename):str(source)}}))
            result=subprocess.run(['go','test','-count=1','-overlay',str(overlay),'-v','./'+package,'-run','^'+test+'$'],cwd=tree,env=os.environ,text=True,capture_output=True)
            print(f'{tree} {package}: exit={result.returncode}',flush=True)
            if result.returncode:
                print(result.stdout+result.stderr)
                sys.exit(result.returncode)
            traces += [base64.b64decode(line.split(' ',1)[1],validate=True) for line in result.stdout.splitlines() if line.startswith('LEGACY-HTTP ')]
        return traces
    before,after=run(BASE),run(ROOT)
    if len(before)!=1304 or len(after)!=1304:
        raise RuntimeError(f'expected 1304 responses, got {len(before)}/{len(after)}')
    for index,(old,new) in enumerate(zip(before,after)):
        if old!=new:
            print(f'response {index} changed:\nbaseline: {old!r}\ncandidate: {new!r}',file=sys.stderr)
            sys.exit(1)
    receipt=Path(sys.argv[2]).resolve() if len(sys.argv)>2 else Path.home()/'.cache/adr0008-6-legacy-http-responses.json'
    receipt.parent.mkdir(parents=True,exist_ok=True)
    receipt.write_text(json.dumps({'baseline':str(BASE),'candidate':str(ROOT),'count':len(before),'responses':[{'index':i,'baseline_base64':base64.b64encode(old).decode(),'candidate_base64':base64.b64encode(new).decode(),'sha256':hashlib.sha256(old).hexdigest()} for i,(old,new) in enumerate(zip(before,after))]},indent=2)+'\n')
    print('PASS: all 1304 complete HTTP responses; Handler + DynamicHandler/Server readiness/policy and brief/change/decisions/discovery (status line, headers, body) are byte-identical; no fields stripped')
    print(f'Complete response receipt: {receipt}')
