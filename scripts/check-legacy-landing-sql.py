#!/usr/bin/env python3
"""Compare production executor/Emit SQL and transaction order against main.

Uses only the shared TEST database (each test allocates its own random schema),
local checkouts, a Go test overlay and the fake forge. No GitHub API calls.
"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
baseline = Path(sys.argv[1]).resolve()
cache = Path.home() / '.cache'
with tempfile.TemporaryDirectory(prefix='sa9b-sql-', dir=cache) as directory:
    directory = Path(directory)
    probe = directory / 'legacy_landing_sql_test.go'
    probe.write_text((root / 'internal/store/legacy_landing_sql_test.go').read_text())
    outputs = []
    for tree in (baseline, root):
        overlay = directory / 'overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(tree / 'internal/store/legacy_landing_sql_test.go'): str(probe)}}))
        result = subprocess.run(['go', 'test', '-overlay', str(overlay), '-count=1', '-v', './internal/store', '-run', '^TestLegacyExecutorEmitSQL$'], cwd=tree, env=os.environ, text=True, capture_output=True)
        print(f'{tree}: exit={result.returncode}')
        if result.returncode:
            print(result.stdout + result.stderr)
            sys.exit(result.returncode)
        records = [line.split('LEGACY-LANDING-SQL ', 1)[1] for line in result.stdout.splitlines() if 'LEGACY-LANDING-SQL ' in line]
        if len(records) != 1:
            raise RuntimeError('missing unique SQL trace')
        outputs.append(json.loads(records[0]))
    if outputs[0] != outputs[1]:
        print(json.dumps(outputs, indent=2))
        sys.exit(1)
    print(f'PASS: identical {len(outputs[0])} statements, including transaction and lock order')
    print(json.dumps(outputs[1], indent=2))
