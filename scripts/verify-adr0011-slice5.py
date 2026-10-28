#!/usr/bin/env python3
"""Run privacy mutants in an isolated archive of HEAD, never the live checkout."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
GO_ROOT = '/home/riels/.local/share/mise/installs/go/1.27.3'
TESTS = ('TestDependency(Privacy|Authorized|Missing|Empty|Source|ExplicitPin|Historical|Unstarted|'
         'Status|DiagnosticReal)|TestDiagnostic')
PREFIX = ['flock', '/home/riels/.cache/openmerge-heavy.lock', 'env', 'GOFLAGS=-p=2',
          'GOMEMLIMIT=4GiB', f'GOROOT={GO_ROOT}', f'PATH={GO_ROOT}/bin:{os.environ["PATH"]}']
MUTANTS = [
    ('one-bit-landed-success', 'internal/dependency/status.go',
     'if !authorized {', 'if false && !authorized {', './internal/status'),
    ('first-reader-only', 'internal/dependency/observation.go',
     'for _, grant := range audience {', 'for _, grant := range audience[:1] {', './internal/status'),
    ('denied-reader-accepted', 'internal/dependency/observation.go',
     'if !grant.valid(o.repositoryID) {', 'if false && !grant.valid(o.repositoryID) {', './internal/status'),
    ('unrelated-repository-grant', 'internal/status/dependencies.go',
     'source.Alias != d.Source ||', 'false ||', './internal/status'),
    ('private-rows-consume-pagination', 'internal/status/status.go',
     'if record.Event == nil {', 'if record.Event == nil && false {', './internal/status'),
    ('clock-rows-consume-pagination', 'internal/status/status.go',
     '; privateClock {', '; privateClock && false {', './internal/status'),
    ('private-journal-id-gaps', 'internal/status/status.go',
     'if j.DependencyRedacted {', 'if j.DependencyRedacted && false {', './internal/status'),
    ('unstarted-batch-success-signal', 'internal/status/dependencies.go',
     'if !j.DependencyRedacted {', 'if true || !j.DependencyRedacted {', './internal/status'),
    ('raw-check-prefix', 'internal/status/dependencies.go',
     'if e == nil || !e.dependencyRedacted || e.Status != "queued" {',
     'if true || e == nil || !e.dependencyRedacted || e.Status != "queued" {', './internal/status'),
    ('api-grant-authorizes-public-check', 'internal/serve/dependency.go',
     'pass.checkAudience, pass.sources', 'pass.audience, pass.sources', './internal/serve'),
    ('historical-current-diagnostic', 'internal/status/change.go',
     'j.DependencyStatus = map[int]store.DependencyStatus{}',
     'j.DependencyStatus = j.DependencyStatus', './internal/status'),
    ('stale-consumer-revision', 'internal/dependency/status.go',
     'd.Event.Revision != revision', 'false', './internal/status'),
    ('diagnostic-code-result-mismatch', 'internal/dependency/diagnostic.go',
     'if o.Result != result ||', 'if false && o.Result != result ||', './internal/dependency'),
    ('diagnostic-wrong-observation', 'internal/dependency/diagnostic.go',
     'current != o ||', 'current != current ||', './internal/dependency'),
    ('redacted-explanation-rows', 'internal/status/status.go',
     '!j.DependencyRedacted && (inheritedPause', 'true && (inheritedPause', './internal/status'),
    ('historical-hold-missing', 'internal/status/change.go',
     'j.DependencyStatus[target] = store.DependencyStatus{Text: "Dependency cannot be verified", Redacted: true, Blocking: true}',
     'delete(j.DependencyStatus, target)', './internal/status'),
    ('consumer-progress-entry', 'internal/status/waiting.go',
     'if v.Redacted {', 'if false && v.Redacted {', './internal/status'),
    ('composed-invalidation-lost', 'internal/status/status.go',
     'if j.DependencyQueue != nil {', 'if false && j.DependencyQueue != nil {', './internal/status'),
    ('unbounded-diagnostic-history', 'internal/dependency/diagnostic.go',
     'if len(next.records) > maxProjectionEntries*MaxDirectEdges {',
     'if false && len(next.records) > maxProjectionEntries*MaxDirectEdges {', './internal/dependency'),
    ('empty-declaration-hold', 'internal/dependency/status.go',
     'if len(d.Event.Edges) == 0 && !d.Held && !d.Unknown {',
     'if false && len(d.Event.Edges) == 0 && !d.Held && !d.Unknown {', './internal/dependency'),
    ('public-watermark-cache-key', 'internal/serve/status_ledger.go',
     'watermark: watermark,', 'watermark: projection.Watermark,', './internal/serve'),
    ('raw-retained-decision-pagination', 'internal/status/dependencies.go',
     'if j.DependencyRedacted {', 'if false && j.DependencyRedacted {', './internal/status'),
    ('decision-scope-bypassed', 'internal/serve/branches.go',
     'if err := s.dependencyDecisionScope(ctx, key); err != nil {',
     'if err := s.dependencyDecisionScope(ctx, key); false && err != nil {', './internal/serve'),
    ('downgrade-inventory-omission', 'internal/store/authority_downgrade.go',
     '"dependency_waits_withheld", "dependency_diagnostic_recorded",',
     '"dependency_waits_withheld",', './internal/store'),
]


def run(checkout, packages):
    env = os.environ.copy()
    env.pop('OPENMERGE_UPDATE_REPORTING_GOLDENS', None)
    for key in ('TEST_DATABASE_URL', 'OPENMERGE_TEST_DATABASE_URL'):
        env[key] = 'postgres://postgres@localhost:54329/postgres?sslmode=disable'
    return subprocess.run(PREFIX + ['go', 'test', '-short', '-count=1', '-timeout=90s',
                                   '-run', TESTS] + packages,
                          cwd=checkout, env=env, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=590)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    results = []
    with tempfile.TemporaryDirectory(prefix='openmerge-slice5-mutants-') as temp:
        archive = Path(temp) / 'head.tar'
        checkout = Path(temp) / 'checkout'
        checkout.mkdir()
        subprocess.run(['git', 'archive', 'HEAD', '--format=tar', f'--output={archive}'],
                       cwd=ROOT, check=True)
        with tarfile.open(archive) as source:
            source.extractall(checkout, filter='data')
        control = run(checkout, ['./internal/status', './internal/dependency',
                                 './internal/serve', './internal/store'])
        if control.returncode:
            raise SystemExit('Control failed:\n' + control.stdout)
        for name, file, old, new, package in MUTANTS:
            path = checkout / file
            original = path.read_text()
            if old not in original:
                raise SystemExit(f'Mutation target missing: {name}')
            try:
                path.write_text(original.replace(old, new, 1))
                result = run(checkout, [package])
                failures = re.findall(r'--- FAIL: (\S+)', result.stdout)
                killed = result.returncode != 0 and bool(failures) and '[build failed]' not in result.stdout and 'panic:' not in result.stdout
                results.append({'mutation': name, 'killed': killed, 'tests': failures})
                print(f'{name}: {"KILLED" if killed else "SURVIVED"}', flush=True)
                if not killed:
                    print(result.stdout, flush=True)
            finally:
                path.write_text(original)
    if args.output:
        args.output.write_text(json.dumps(results, indent=2) + '\n')
    if not all(item['killed'] for item in results):
        raise SystemExit('A mutant survived or failed without an assertion failure')


if __name__ == '__main__':
    main()
