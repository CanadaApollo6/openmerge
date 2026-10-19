#!/usr/bin/env python3
"""Compare legacy check bytes and old status fields against an explicit checkout.

Uses deterministic status fixtures and Go overlays under ~/.cache. No DB,
forge, live status API, fetch or module download is performed.
"""
import base64
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
 "encoding/base64"
 "encoding/json"
 "fmt"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/queue"
 // CANDIDATE_IMPORT
)
func TestLegacyReportingProbe(t *testing.T) {
 legacyGolden := append(ledgerChain(), identified("b1",1,1,"success"))
 multiEntry := append(ledgerChain(), identified("b1",1,1,"success"), identified("b2",2,1,"pending"))
 landed := append(admission(), queue.BatchStarted{BatchID:"b1",BaseSHA:"base",PRs:[]queue.BatchPR{{PR:1,HeadSHA:"head"}},At:at}, queue.TestRefBuilt{BatchID:"b1",TestedSHA:"tested-landed",At:at,Method:queue.Merge}, queue.ChecksCompleted{TestedSHA:"tested-landed",Success:true}, queue.LandingStarted{BatchID:"b1",AttemptID:"attempt",FenceToken:"fence",At:at}, queue.Landed{BatchID:"b1",NewBaseSHA:"tested-landed",NewFence:"next"})
 dequeued := append(admission(), queue.DequeueRequested{PR:1}, queue.Dequeued{PR:1,AdmissionID:1,Reason:queue.Requested})
 blocked := []queue.Event{queue.PRObserved{PR:7,HeadSHA:"fork",Eligible:true},queue.EnqueueRequested{PR:7,At:at}}
 depth4 := []queue.Event{queue.BaseMoved{SHA:"base"}}
 for pr:=1;pr<=4;pr++ {
  depth4=append(depth4,queue.PRObserved{PR:pr,HeadSHA:"head",Eligible:true,Trusted:true},queue.EnqueueRequested{PR:pr,At:at})
  id:=fmt.Sprintf("b%d",pr); parent:=""; base:="base"
  if pr>1 { parent=fmt.Sprintf("b%d",pr-1); base=fmt.Sprintf("tested-b%d",pr-1) }
  depth4=append(depth4,queue.ChainedBatchStarted{BatchID:id,BaseSHA:base,PRs:[]queue.BatchPR{{PR:pr,HeadSHA:"head"}},At:at,ParentBatchID:parent,Position:uint64(pr)},queue.ChainedTestRefBuilt{BatchID:id,TestedSHA:"tested-"+id,LandingSHA:"tested-"+id,ParentBatchID:parent,Position:uint64(pr),Method:queue.Merge,At:at})
 }
 fixtures:=[]struct{name string; events []queue.Event; pr int}{
  {"legacy-golden",legacyGolden,1},
  {"multi-entry",multiEntry,2},
  {"landed",landed,1},
  {"dequeued",dequeued,1},
  {"blocked",blocked,7},
  {"depth4",depth4,4},
 }
 for _,tc:=range fixtures {
  j:=journal(tc.events...)
  q,e,why:=Project(j,tc.pr,100,nil)
  switch tc.name {
  case "landed": if e==nil || e.Status!="merged" { t.Fatalf("landed fixture did not land: %+v",e) }
  case "dequeued": if e==nil || e.Status!="dequeued" { t.Fatalf("dequeued fixture remained admitted: %+v",e) }
  case "blocked": if e==nil || e.Status!="blocked" { t.Fatalf("blocked fixture was admitted: %+v",e) }
  case "depth4": if len(q.Chain)!=4 { t.Fatalf("depth4 fixture has %d batches",len(q.Chain)) }
  case "multi-entry": if len(q.Entries)<2 { t.Fatal("multi-entry fixture is not populated") }
  }
  ApplyPolicy(&q,e,queue.Config{SpeculationMaxDepth:4,BatchMaxSize:8},at)
  suffix:=""
  // CANDIDATE_SUFFIX
  check:="\\n\\nQueue: "+AdaptiveSummary(&q)+LedgerSummary(e)+suffix
  raw,err:=json.Marshal(Response{Version:Version,Queue:&q,PR:e,Why:why})
  if err!=nil { t.Fatal(err) }
  check64:=base64.StdEncoding.EncodeToString([]byte(check))
  status64:=base64.StdEncoding.EncodeToString(raw)
  fmt.Printf("LEGACY-REPORTING %s %s %s\\n",tc.name,check64,status64)
 }
}
'''


def strip_additive_fields(raw):
    """Remove only Entry.change and Entry.landing, preserving all other bytes/order."""
    def string_end(start):
        pos = start + 1
        while pos < len(raw):
            if raw[pos] == ord('\\'):
                pos += 2
            elif raw[pos] == ord('"'):
                return pos + 1
            else:
                pos += 1
        raise ValueError('unterminated JSON string')

    def value(start, path=()):
        token = raw[start]
        if token == ord('"'):
            end = string_end(start)
            return raw[start:end], end
        if token == ord('{'):
            pos = start + 1
            members = []
            if raw[pos] == ord('}'):
                return b'{}', pos + 1
            while True:
                key_start = pos
                key_end = string_end(pos)
                key = json.loads(raw[key_start:key_end])
                if raw[key_end] != ord(':'):
                    raise ValueError('invalid compact JSON object')
                item, pos = value(key_end + 1, path + (key,))
                if key not in ('change', 'landing') or path not in (('pr',), ('queue', 'entries', '[]'), ('queues', '[]', 'entries', '[]')):
                    members.append(raw[key_start:key_end] + b':' + item)
                if raw[pos] == ord('}'):
                    return b'{' + b','.join(members) + b'}', pos + 1
                if raw[pos] != ord(','):
                    raise ValueError('invalid compact JSON object separator')
                pos += 1
        if token == ord('['):
            pos = start + 1
            items = []
            if raw[pos] == ord(']'):
                return b'[]', pos + 1
            while True:
                item, pos = value(pos, path + ('[]',))
                items.append(item)
                if raw[pos] == ord(']'):
                    return b'[' + b','.join(items) + b']', pos + 1
                if raw[pos] != ord(','):
                    raise ValueError('invalid compact JSON array separator')
                pos += 1
        pos = start
        while pos < len(raw) and raw[pos] not in b',}]':
            pos += 1
        return raw[start:pos], pos

    stripped, end = value(0)
    if end != len(raw):
        raise ValueError('trailing bytes after JSON status')
    return stripped


def equivalent(old_check, old_status, new_check, new_status):
    return old_check == new_check and strip_additive_fields(old_status) == strip_additive_fields(new_status)


def negative_controls():
    old = b'{"version":1,"pr":{"position":1,"status":"merged"}}'
    allowed = b'{"version":1,"pr":{"landing":{"outcome":"landed"},"change":{"id":"change-1"},"position":1,"status":"merged"}}'
    if strip_additive_fields(old) != strip_additive_fields(allowed):
        raise RuntimeError('negative control setup rejected the two permitted additive fields')
    controls = {
        'old field order': b'{"version":1,"pr":{"status":"merged","position":1}}',
        'old field value': b'{"version":1,"pr":{"position":1,"status":"dequeued"}}',
        'non-entry additive name': b'{"version":1,"change":true,"pr":{"position":1,"status":"merged"}}',
        'unapproved field': b'{"version":1,"pr":{"position":1,"extra":true,"status":"merged"}}',
    }
    for label, changed in controls.items():
        if strip_additive_fields(old) == strip_additive_fields(changed):
            raise RuntimeError('legacy comparator failed its ' + label + ' negative control')
    if equivalent(b'check', old, b'check changed', allowed):
        raise RuntimeError('legacy comparator failed its check-byte negative control')


negative_controls()
with tempfile.TemporaryDirectory(prefix='sa12a-reporting-', dir=Path.home()/'.cache') as tmp:
    directory = Path(tmp)

    def run(tree, candidate):
        source = PROBE
        if candidate:
            source = source.replace('// CANDIDATE_IMPORT', 'cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"')
            source = source.replace('// CANDIDATE_SUFFIX', 'var err error; suffix,err=ReviewerCheckSummary(j,tc.pr,cp.Policy{});if err!=nil { t.Fatal(err) }')
        else:
            source = source.replace('// CANDIDATE_IMPORT', '')
            source = source.replace('// CANDIDATE_SUFFIX', '')
        path = directory/'probe.go'
        path.write_text(source)
        overlay = directory/'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(tree/'internal/status/legacy_reporting_probe_test.go'):str(path)}}))
        result = subprocess.run(['go','test','-count=1','-overlay',str(overlay),'-v','./internal/status','-run','^TestLegacyReportingProbe$'],cwd=tree,env=os.environ,text=True,capture_output=True)
        print(f'{tree}: exit={result.returncode}',flush=True)
        if result.returncode:
            print(result.stdout+result.stderr)
            sys.exit(result.returncode)
        traces = {}
        for line in result.stdout.splitlines():
            if not line.startswith('LEGACY-REPORTING '):
                continue
            _, name, check64, status64 = line.split(' ', 3)
            if name in traces:
                raise RuntimeError(f'duplicate reporting trace for {name}')
            check = base64.b64decode(check64, validate=True)
            status = base64.b64decode(status64, validate=True)
            json.loads(status)
            traces[name] = (check, status)
        return traces

    before = run(BASE,False)
    after = run(ROOT,True)
    expected = {'legacy-golden','multi-entry','landed','dequeued','blocked','depth4'}
    if set(before) != expected or set(after) != expected:
        raise RuntimeError(f'fixture mismatch: baseline={sorted(before)} candidate={sorted(after)}')
    for name in sorted(expected):
        old_check, old_status = before[name]
        new_check, new_status = after[name]
        if not equivalent(old_check, old_status, new_check, new_status):
            print(f'legacy reporting changed for {name}', file=sys.stderr)
            print('baseline check:', old_check.decode('utf-8'), file=sys.stderr)
            print('candidate check:', new_check.decode('utf-8'), file=sys.stderr)
            print('baseline status:', old_status.decode('utf-8'), file=sys.stderr)
            print('candidate status:', new_status.decode('utf-8'), file=sys.stderr)
            sys.exit(1)
    golden = ROOT/'internal/status/testdata/legacy-check.txt'
    if '--record' in sys.argv[2:]:
        golden.parent.mkdir(parents=True,exist_ok=True)
        golden.write_bytes(before['legacy-golden'][0])
    if golden.read_bytes() != before['legacy-golden'][0]:
        raise RuntimeError('legacy check differs from golden captured from main')
    print('PASS: six legacy checks are byte-identical; serialized preexisting status fields and order match after stripping only change/landing',flush=True)
