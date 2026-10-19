#!/usr/bin/env python3
"""Compare Astra's legacy status fixture with an explicit local main checkout.

Seven serial reads per checkout; require median latency and allocated bytes to
stay within 2x main. No forge, database, fetch, or live status API access.
Use the normal shared-host test environment and run only when the host is clear.
"""
import json
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PROBE = r'''package status
import("testing";"fmt";"time";"runtime";"net/http/httptest";"github.com/CanadaApollo6/openmerge/internal/queue";"github.com/CanadaApollo6/openmerge/internal/store")
func TestReportingPerformanceProbe(t *testing.T){
 events:=[]queue.Event{queue.BaseMoved{SHA:"base"}}
 for i:=1;i<=128;i++{events=append(events,queue.PRObserved{PR:i,HeadSHA:"head",Eligible:true,Trusted:true},queue.EnqueueRequested{PR:i,At:at})}
 for i:=0;i<512;i++{events=append(events,queue.RefreshRequested{})}
 j:=journal(events...);h:=Handler(&source{journals:[]store.Journal{j}},[]store.QueueKey{key},"",nil)
 runtime.GC();var before,after runtime.MemStats;runtime.ReadMemStats(&before);start:=time.Now()
 w:=httptest.NewRecorder();h.ServeHTTP(w,httptest.NewRequest("GET","/v1/queues/owner/repo/main/prs/1",nil))
 elapsed:=time.Since(start);runtime.ReadMemStats(&after)
 if w.Code!=200{t.Fatal(w.Code)}
 fmt.Printf("REPORTING-PERF ns=%d allocated=%d\n",elapsed.Nanoseconds(),after.TotalAlloc-before.TotalAlloc)
}
'''


def measure(tree, directory):
    source = directory / 'probe.go'
    source.write_text(PROBE)
    overlay = directory / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(tree / 'internal/status/reporting_performance_probe_test.go'): str(source)}}))
    result = subprocess.run(['go', 'test', '-overlay', str(overlay), '-count=7', '-v', './internal/status', '-run', '^TestReportingPerformanceProbe$'], cwd=tree, env=os.environ, text=True, capture_output=True)
    print(f'{tree}: actual exit={result.returncode}', flush=True)
    if result.returncode:
        print(result.stdout + result.stderr)
        sys.exit(result.returncode)
    rows = [(int(ns), int(allocated)) for ns, allocated in re.findall(r'REPORTING-PERF ns=(\d+) allocated=(\d+)', result.stdout)]
    if len(rows) != 7:
        raise RuntimeError(f'expected seven measurements, got {rows}')
    print(json.dumps({'checkout': str(tree), 'samples': rows}), flush=True)
    return statistics.median(ns for ns, _ in rows), statistics.median(allocated for _, allocated in rows)


if len(sys.argv) != 2:
    sys.exit('usage: check-reporting-performance.py /explicit/main/checkout')
with tempfile.TemporaryDirectory(prefix='reporting-perf-', dir=Path.home() / '.cache') as tmp:
    baseline = measure(Path(sys.argv[1]).resolve(), Path(tmp))
    candidate = measure(ROOT, Path(tmp))
print(f'median main: {baseline[0] / 1e6:.3f} ms / {baseline[1]:.0f} bytes; candidate: {candidate[0] / 1e6:.3f} ms / {candidate[1]:.0f} bytes', flush=True)
print(f'candidate/main latency={candidate[0] / baseline[0]:.3f}, allocations={candidate[1] / baseline[1]:.3f}', flush=True)
if candidate[0] > 2 * baseline[0] or candidate[1] > 2 * baseline[1]:
    sys.exit('reporting performance exceeds 2x main budget')
print('PASS: legacy status latency and allocated bytes are within 2x main', flush=True)
