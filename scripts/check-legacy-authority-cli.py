#!/usr/bin/env python3
"""Compare complete CLI status text against a base checkout, through Journals and Project.

Run under the heavy-command flock with GOFLAGS/GOMEMLIMIT set. Uses Go overlays
in ~/.cache; no fields or whitespace are stripped from the compared output.
"""
import base64
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
from legacy_authority_fixtures import GO_IMPORTS, GO_FIXTURES

ROOT = Path(__file__).resolve().parents[1]
BASE = Path(sys.argv[1]).resolve()
PROBE = "package main\nimport (\n" + GO_IMPORTS + r'''
 "bytes"
 "encoding/base64"
 "github.com/CanadaApollo6/openmerge/internal/status"
)
''' + GO_FIXTURES + r'''
func TestLegacyAuthorityCLIProbe(t *testing.T) {
 for _,kind:=range []string{"empty","idle","waiting","paused","active","outside"} {
  db,key:=legacyDBFixture(t,kind)
  journals,err:=db.Journals(t.Context(),[]store.QueueKey{key});if err!=nil {t.Fatal(err)}
  response:=status.Response{Version:1,Queues:[]status.Queue{}}
  for _,j:=range journals {
   q,_,_:=status.Project(j,0,0,nil)
   if kind=="waiting" {q.Readiness=&status.Readiness{Ready:false,Reason:"validation pending"}}
   response.Queues=append(response.Queues,q)
  }
  var out bytes.Buffer
  if err:=printStatus(&out,response);err!=nil { t.Fatal(err) }
  fmt.Printf("LEGACY-CLI %s %s\n",kind,base64.StdEncoding.EncodeToString(out.Bytes()))
 }
}
'''
with tempfile.TemporaryDirectory(prefix='authority-cli-',dir=Path.home()/'.cache') as tmp:
    directory=Path(tmp)
    probe=directory/'probe.go'
    probe.write_text(PROBE)
    def run(tree):
        overlay=directory/'overlay.json'
        overlay.write_text(json.dumps({'Replace':{str(tree/'cmd/openmerge/legacy_authority_cli_probe_test.go'):str(probe)}}))
        result=subprocess.run(['go','test','-count=1','-overlay',str(overlay),'-v','./cmd/openmerge','-run','^TestLegacyAuthorityCLIProbe$'],cwd=tree,env=os.environ,text=True,capture_output=True)
        print(f'{tree}: exit={result.returncode}',flush=True)
        if result.returncode:
            print(result.stdout+result.stderr)
            sys.exit(result.returncode)
        return {parts[1]:base64.b64decode(parts[2],validate=True) for line in result.stdout.splitlines() if line.startswith('LEGACY-CLI ') for parts in [line.split(' ',2)]}
    before,after=run(BASE),run(ROOT)
    if len(before)!=6 or before!=after:
        raise RuntimeError(f'CLI bytes changed: {before!r} / {after!r}')
    receipt=Path.home()/'.cache/adr0008-6-legacy-cli-responses.json'
    receipt.write_text(json.dumps({name:{'baseline_base64':base64.b64encode(raw).decode(),'candidate_base64':base64.b64encode(after[name]).decode(),'sha256':hashlib.sha256(raw).hexdigest()} for name,raw in before.items()},indent=2)+'\n')
    print('PASS: six complete CLI status outputs are byte-identical; no fields or whitespace stripped')
    print(f'Complete CLI receipt: {receipt}')
