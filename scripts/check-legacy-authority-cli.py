#!/usr/bin/env python3
"""Compare complete CLI status text against a base checkout, without DB/network.

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

ROOT = Path(__file__).resolve().parents[1]
BASE = Path(sys.argv[1]).resolve()
PROBE = r'''package main
import (
 "bytes"
 "encoding/base64"
 "fmt"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/status"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestLegacyAuthorityCLIProbe(t *testing.T) {
 for _,kind:=range []string{"empty","idle","waiting","paused","active","outside"} {
  q:=status.Queue{Repository:"owner/repo",Base:"main",State:"active"}
  switch kind {
  case "waiting":q.Readiness=&status.Readiness{Ready:false,Reason:"validation pending"}
  case "paused":q.Paused=true;q.PauseReason="operator";q.Blocks=[]status.Block{{Source:"operator",Reason:"operator",Detail:"fixture"}}
  case "active":q.Entries=[]status.Entry{{Position:1,PR:42,HeadSHA:"0123456789abcdef",Status:"testing",Reason:"ci pending"}}
  case "outside":q.OutsidePushes=[]status.OutsidePush{{EventID:12,OutsidePushRecorded:queue.OutsidePushRecorded{PreviousSHA:"base",NewSHA:"outside",AncestryKnown:true,HistoryKnown:true,Commits:[]string{"outside"},VerificationBeforeLanding:"none"}}}
  }
  response:=status.Response{Version:1,Queue:&q}
  if kind=="empty" { response=status.Response{Version:1,Queues:[]status.Queue{}} }
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
    receipt=Path.home()/'.cache/adr0008-7-legacy-cli-responses.json'
    receipt.write_text(json.dumps({name:{'baseline_base64':base64.b64encode(raw).decode(),'candidate_base64':base64.b64encode(after[name]).decode(),'sha256':hashlib.sha256(raw).hexdigest()} for name,raw in before.items()},indent=2)+'\n')
    print('PASS: six complete CLI status outputs are byte-identical; no fields or whitespace stripped')
    print(f'Complete CLI receipt: {receipt}')
