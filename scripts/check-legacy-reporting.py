#!/usr/bin/env python3
"""Compare legacy check bytes and old status fields against an explicit checkout.

Uses only deterministic status fixtures and Go overlays under ~/.cache. No DB,
forge, live status API, fetch or module download is performed.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
BASE = Path(sys.argv[1]).resolve()
PROBE = '''package status
import (
 "encoding/json"
 "fmt"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestLegacyReportingProbe(t *testing.T) {
 j:=journal(append(ledgerChain(),identified("b1",1,1,"success"))...)
 q,e,why:=Project(j,1,3,nil)
 ApplyPolicy(&q,e,queue.Config{SpeculationMaxDepth:4,BatchMaxSize:8},at)
 suffix:=""
 // CANDIDATE_SUFFIX
 check:="\\n\\nQueue: "+AdaptiveSummary(&q)+LedgerSummary(e)+suffix
 raw,err:=json.Marshal(struct { Check string; Status Response }{check,Response{Version:Version,Queue:&q,PR:e,Why:why}})
 if err!=nil {t.Fatal(err)}
 fmt.Printf("LEGACY-REPORTING %s\\n",raw)
}
'''
with tempfile.TemporaryDirectory(prefix='sa12a-reporting-', dir=Path.home()/'.cache') as tmp:
    directory = Path(tmp)
    def run(tree, candidate):
        source = PROBE
        if candidate:
            source = source.replace('// CANDIDATE_SUFFIX', 'var err error; suffix,err=ReviewerCheckSummary(j,1,cp.Policy{});if err!=nil {t.Fatal(err)}')
            source = source.replace('"testing"', '"testing"\n cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"')
            source = source.replace('raw,err:=', 'raw,err:=')
        path = directory/'probe.go'
        path.write_text(source)
        overlay = directory/'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(tree/'internal/status/legacy_reporting_probe_test.go'):str(path)}}))
        result = subprocess.run(['go','test','-count=1','-overlay',str(overlay),'-v','./internal/status','-run','^TestLegacyReportingProbe$'],cwd=tree,env=os.environ,text=True,capture_output=True)
        print(f'{tree}: exit={result.returncode}',flush=True)
        if result.returncode:
            print(result.stdout+result.stderr)
            sys.exit(result.returncode)
        lines = [line.split('LEGACY-REPORTING ',1)[1] for line in result.stdout.splitlines() if line.startswith('LEGACY-REPORTING ')]
        if len(lines)!=1:
            raise RuntimeError('missing unique reporting trace')
        return json.loads(lines[0])
    before = run(BASE,False)
    after = run(ROOT,True)
    # Only the two explicitly new additive entry fields may differ.
    for response in [after['Status']]:
        for entry in response['queue']['entries']+[response['pr']]:
            entry.pop('change',None)
            entry.pop('landing',None)
    if before!=after:
        print(json.dumps([before,after],indent=2))
        sys.exit(1)
    golden = ROOT/'internal/status/testdata/legacy-check.txt'
    if '--record' in sys.argv[2:]:
        golden.parent.mkdir(parents=True,exist_ok=True)
        golden.write_text(before['Check'])
    if golden.read_text()!=before['Check']:
        raise RuntimeError('legacy check differs from golden captured from main')
    print('PASS: byte-identical legacy check and identical old status fields; only new additive JSON fields differ',flush=True)
