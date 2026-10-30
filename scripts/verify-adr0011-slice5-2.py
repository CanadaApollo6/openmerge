#!/usr/bin/env python3
"""Reintroduce ADR 0011 5-2 and 5-1 privacy leaks one at a time.

Each leak runs only the metamorphic tests, with -short, against an isolated
archive of HEAD through go test -overlay. The active checkout is never edited.
A leak counts as caught only on an assertion failure: build failures, panics,
timeouts and connection errors are reported as invalid.
"""
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
PREFIX = ['flock', '/home/riels/.cache/openmerge-heavy.lock', 'env', 'GOFLAGS=-p=2',
          'GOMEMLIMIT=4GiB', f'GOROOT={GO_ROOT}', f'PATH={GO_ROOT}/bin:{os.environ["PATH"]}']
SUITES = {
    'status': ('./internal/status', 'TestDependencyMetamorphicPrivacy$'),
    'cli': ('./cmd/openmerge', 'TestDependencyMetamorphicCLI$'),
    'serve': ('./internal/serve', 'TestDependencyMetamorphicPublication$'),
}
STATUS = 'internal/status/dependencies.go'
MASK_END = '\t\tview.Gate = "dequeue"\n\t}\n\t*e = view\n}'


def keep(condition, fields):
    return MASK_END.replace('\t*e = view', f'\tif {condition} {{\n\t\t{fields}\n\t}}\n\t*e = view')


# (row, leak, [(file, old, new)], suites)
NEW = [
    ('N1', 'wait-age starting at private eligibility in the generic view',
     [(STATUS, 'v.Redacted || !v.Blocking', '!v.Blocking')], ['status']),
    ('N2', 'private-dependent records in the redacted export (counts, order, cursors, totals)',
     [(STATUS, '\tdefault:\n\t\treturn audit.DependencyEntry{}, false', '\tdefault:\n\t\tevidence = struct{}{}')], ['status']),
    ('N2b', 'N2 with only cursor pages compared (pagination channel alone)',
     [(STATUS, '\tdefault:\n\t\treturn audit.DependencyEntry{}, false', '\tdefault:\n\t\tevidence = struct{}{}'),
      ('internal/status/dependencies_metamorphic_test.go', 'out := map[string][]byte{"export:manifest": manifest, "export:records": stream}',
       'out := map[string][]byte{}'),
      ('internal/status/dependencies_metamorphic_test.go', '\t\t\t\tverify()\n', '\t\t\t\t_ = verify\n'),
      ('internal/status/dependencies_metamorphic_test.go', '\tif !bytes.Equal(manifest, tailManifest) || !bytes.Equal(stream, tailStream) {',
       '\tif false && (!bytes.Equal(manifest, tailManifest) || !bytes.Equal(stream, tailStream)) {')], ['status']),
    ('N3', 'recording times on redacted export records',
     [(STATUS, '\t\t\tredacted = append(redacted, entry)',
       '\t\t\tentry.Source = &audit.DependencySource{RecordedAt: record.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000Z")}\n\t\t\tredacted = append(redacted, entry)')], ['status']),
    ('N3b', 'N3 with strict verification bypassed (timestamp channel alone)',
     [(STATUS, '\t\t\tredacted = append(redacted, entry)',
       '\t\t\tentry.Source = &audit.DependencySource{RecordedAt: record.RecordedAt.UTC().Format("2006-01-02T15:04:05.000000Z")}\n\t\t\tredacted = append(redacted, entry)'),
      ('internal/status/dependencies_metamorphic_test.go', '\t\t\t\tverify()\n', '\t\t\t\t_ = verify\n')], ['status']),
    ('N4', 'journal event reference on the unauthorized self-acknowledgement',
     [(STATUS, '\t\tif !redacted {', '\t\tif true {')], ['status']),
    ('N5', 'multi-writer warning present only while the private wait blocks',
     [('internal/dependency/status.go', '\tif !authorized {\n\t\treturn generic, true',
       '\tif !authorized {\n\t\tif s.Admission(d.Event.ChangeID, d.Event.Revision).Code != "" && s.Hold(d.Event.Alias).MultiWriterWarning {\n\t\t\tgeneric.Warnings = []string{MultiWriterWarning}\n\t\t}\n\t\treturn generic, true')], ['status']),
    ('N6', 'complete export granted without complete source authorization',
     [(STATUS, 'complete = complete && sourceAuthorized(edge.Reference, scope, audience, sources)',
       'complete = complete || sourceAuthorized(edge.Reference, scope, audience, sources)')], ['status']),
]

# The ten #202 round-5 rows, reconstructed on this head.
OLD = [
    ('1', '`predecessor-build` gate on an independent PR',
     [(STATUS, MASK_END, keep('e.Gate == "predecessor-build"', 'view.Gate = e.Gate'))], ['status']),
    ('2', 'check cache survives revocation',
     [('internal/serve/status_ledger.go', ' || w.ledgerCache.authorization != authorization', '')], ['serve']),
    ('3', 'co-batched `dependency-changed` reason',
     [(STATUS, MASK_END, keep('e.Reason == "dependency-changed"', 'view.Reason = e.Reason'))], ['status']),
    ('4', '`dequeue-pending` batch ID, position and ahead',
     [(STATUS, '\t\tview.Gate = "dequeue"\n', '\t\tview.Gate = "dequeue"\n\t\tview.BatchID, view.BatchPosition, view.BatchesAhead = e.BatchID, e.BatchPosition, e.BatchesAhead\n')], ['status']),
    ('5', 'duplicate publication under the authority pin',
     [('internal/serve/worker.go', '\t\t\t\tif admitted {', '\t\t\t\tif admitted && w.dependencies == nil {')], ['serve']),
    ('6', '`DependencyCheckStatus` bypass for `dequeue-pending`',
     [(STATUS, '\tif !q.dependencyRedacted {\n\t\treturn previous', '\tif !q.dependencyRedacted || e != nil && e.Status == "dequeue-pending" {\n\t\treturn previous')], ['serve']),
    ('7', 'batch-derived `Detail`',
     [(STATUS, MASK_END, keep('e.Status == "dequeue-pending" || e.Status == "paused"', 'view.Detail = e.Detail'))], ['status']),
    ('8', 'landing-batch metadata before a confirmed merge',
     [(STATUS, '\tq.Chain = nil\n\tq.ActiveBatch = nil',
       '\tvar kept []*Batch\n\tfor _, b := range q.Chain {\n\t\tif b.Phase == "landing" {\n\t\t\tkept = append(kept, b)\n\t\t}\n\t}\n\tq.Chain = kept\n\tif q.ActiveBatch != nil && q.ActiveBatch.Phase != "landing" {\n\t\tq.ActiveBatch = nil\n\t}')], ['status']),
    ('9', 'unconfirmed landing status (revert 9176c96b)',
     [(STATUS, 'case "queued", "building", "testing", "invalidating", "landing":', 'case "queued", "building", "testing", "invalidating":'),
      (STATUS, '\tcase "paused":\n\t\tstate = forge.Paused\n', '\tcase "paused":\n\t\tstate = forge.Paused\n\tcase "landing":\n\t\tstate = forge.Landing\n')], ['status', 'cli', 'serve']),
    ('10', 'CI gates `landing-preflight` / `bisection`',
     [(STATUS, MASK_END, keep('e.Gate == "landing-preflight" || e.Gate == "bisection"', 'view.Gate = e.Gate'))], ['status']),
]

INVALID = ('[build failed]', 'panic:', 'connect: connection refused', 'failed to connect to',
           'context deadline exceeded', 'test timed out')


def run(checkout, suite):
    package, tests = SUITES[suite]
    env = os.environ.copy()
    for key in ('TEST_DATABASE_URL', 'OPENMERGE_TEST_DATABASE_URL'):
        env[key] = 'postgres://postgres@localhost:54329/postgres?sslmode=disable'
    return subprocess.run(PREFIX + ['go', 'test', '-overlay', str(checkout / 'overlay.json'), '-short', '-count=1',
                                    '-failfast', '-timeout=480s', '-run', tests, package],
                          cwd=checkout, env=env, text=True, stdout=subprocess.PIPE,
                          stderr=subprocess.STDOUT, timeout=590)


def detail(output):
    for line in output.splitlines():
        line = line.strip()
        if re.match(r'^\S+_test\.go:\d+: ', line):
            return line[:240]
    return ''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--rows', help='comma-separated rows, e.g. N1,9')
    parser.add_argument('--no-serve', action='store_true', help='skip Postgres-backed serve runs')
    args = parser.parse_args()
    selected = set(args.rows.split(',')) if args.rows else None
    rows = [r for r in NEW + OLD if selected is None or r[0] in selected]
    results = []
    cache = Path.home() / '.cache'
    with tempfile.TemporaryDirectory(prefix='slice5-2-mutants-', dir=cache) as temp:
        archive = Path(temp) / 'head.tar'
        checkout = Path(temp) / 'checkout'
        checkout.mkdir()
        subprocess.run(['git', 'archive', 'HEAD', '--format=tar', f'--output={archive}'], cwd=ROOT, check=True)
        replacements = {}
        with tarfile.open(archive) as source:
            for member in source.getmembers():
                if member.isfile() and member.name.endswith('.go'):
                    virtual = checkout / member.name
                    member.name += '.source'
                    replacements[str(virtual)] = str(checkout / member.name)
                source.extract(member, checkout, filter='data')
        (checkout / 'overlay.json').write_text(json.dumps({'Replace': replacements}))
        suites = sorted({s for r in rows for s in r[3] if not (args.no_serve and s == 'serve')})
        for suite in suites:
            control = run(checkout, suite)
            if control.returncode:
                raise SystemExit(f'Control failed ({suite}):\n' + control.stdout)
            print(f'control {suite}: pass', flush=True)
        for row, leak, edits, row_suites in rows:
            originals = {}
            try:
                for file, old, new in edits:
                    path = checkout / (file + '.source')
                    if path not in originals:
                        originals[path] = path.read_text()
                    text = path.read_text()
                    if text.count(old) != 1:
                        raise SystemExit(f'Mutation target for row {row} not unique in {file}: {old!r}')
                    path.write_text(text.replace(old, new, 1))
                for suite in row_suites:
                    if args.no_serve and suite == 'serve':
                        continue
                    result = run(checkout, suite)
                    failures = re.findall(r'--- FAIL: (\S+)', result.stdout)
                    invalid = any(marker in result.stdout for marker in INVALID)
                    caught = result.returncode != 0 and bool(failures) and not invalid
                    item = {'row': row, 'leak': leak, 'suite': suite, 'caught': caught,
                            'invalid': invalid, 'test': failures[-1] if failures else '', 'assertion': detail(result.stdout)}
                    results.append(item)
                    print(f'{row} {suite}: {"CAUGHT" if caught else "INVALID" if invalid else "MISSED"} {item["test"]}\n    {item["assertion"]}', flush=True)
                    if not caught:
                        print(result.stdout[-4000:], flush=True)
            finally:
                for path, text in originals.items():
                    path.write_text(text)
    if args.output:
        args.output.write_text(json.dumps(results, indent=2) + '\n')
    if not all(item['caught'] for item in results):
        raise SystemExit('A leak was not caught by an assertion')


if __name__ == '__main__':
    main()
