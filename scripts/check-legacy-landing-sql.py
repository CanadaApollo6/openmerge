#!/usr/bin/env python3
"""Compare real executor/Emit SQL; prove sensitivity with five overlay mutants.

Only isolated test DB schemas, local checkouts and fake forge are used.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
baseline = Path(sys.argv[1]).resolve()


def replace_once(source, old, new):
    if source.count(old) != 1:
        raise RuntimeError(f'mutation anchor count {source.count(old)}: {old}')
    return source.replace(old, new, 1)


with tempfile.TemporaryDirectory(prefix='sa9b-sql-', dir=Path.home() / '.cache') as tmp:
    directory = Path(tmp)
    probe = directory / 'legacy_landing_sql_test.go'
    probe.write_text((root / 'internal/store/legacy_landing_sql_test.go').read_text())

    def trace(tree, label, source=None):
        replacements = {str(tree / 'internal/store/legacy_landing_sql_test.go'): str(probe)}
        if source is not None:
            mutant = directory / 'store.go'
            mutant.write_text(source)
            replacements[str(tree / 'internal/store/store.go')] = str(mutant)
        overlay = directory / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': replacements}))
        run = subprocess.run(['go', 'test', '-overlay', str(overlay), '-count=1', '-v', './internal/store', '-run', '^TestLegacyExecutorEmitSQL$'], cwd=tree, env=os.environ, text=True, capture_output=True)
        print(f'{label}: test exit={run.returncode}', flush=True)
        if run.returncode:
            print(run.stdout + run.stderr)
            sys.exit(run.returncode)
        records = [line.split('LEGACY-LANDING-SQL ', 1)[1] for line in run.stdout.splitlines() if 'LEGACY-LANDING-SQL ' in line]
        if len(records) != 1:
            raise RuntimeError('missing unique SQL trace')
        return json.loads(records[0])

    expected = trace(baseline, str(baseline))
    actual = trace(root, str(root))
    if expected != actual:
        print(json.dumps([expected, actual], indent=2))
        sys.exit(1)
    print(f'PASS: identical {len(actual)} statements, including transaction and lock order', flush=True)
    print(json.dumps(actual, indent=2), flush=True)
    original = (root / 'internal/store/store.go').read_text()
    authorize = 'func (s *Store) authorizeStart(ctx context.Context, tx pgx.Tx, execution emission, event queue.Event) (bool, error) {'
    emit = 'return func(e queue.Event) error {'
    mutants = {
        'authorize-extra-statement': replace_once(original, authorize, authorize + '\n if _, err := tx.Exec(ctx, "SELECT 1"); err != nil { return false, err }'),
        'claim-without-for-update': replace_once(original, "AND status = 'in_flight' AND lease_until > $4 FOR UPDATE", "AND status = 'in_flight' AND lease_until > $4"),
        'legacy-via-nil-policy-start': replace_once(original, emit, emit + '\n if start, ok := e.(queue.LandingStarted); ok { return s.StartLanding(ctx, claim, cfg, cp.Policy{}, nil, nil, start) }'),
        'load-before-claim-check': replace_once(original, authorize, authorize + '\n if _, _, err := load(ctx, tx, execution.claim.QueueID); err != nil { return false, err }'),
        'emit-send-batch': replace_once(original, emit, emit + '\n batch := &pgx.Batch{}; batch.Queue("SELECT 1"); if err := s.pool.SendBatch(ctx,batch).Close(); err != nil { return err }'),
    }
    for name, source in mutants.items():
        observed = trace(root, name, source)
        if observed == expected:
            print(f'FAIL: negative control {name} escaped detection')
            sys.exit(1)
        print(f'PASS: negative control {name}: detected SQL difference (expected rejection)', flush=True)
