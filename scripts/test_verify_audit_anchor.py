"""Contract cases; invoked by Go so the existing CI runs them."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('verify-audit-anchor.py')


class Contract(unittest.TestCase):
    def test_contract(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            a, b = root / 'a', root / 'b'
            a.mkdir()
            b.mkdir()
            manifest = dict(version=1, manifest_sha256='a' * 64, request=dict(
                repository='owner/repo', installation='7',
                **{'from': '2026-10-22T00:00:00.000000Z', 'until': '2026-10-23T00:00:00.000000Z'}))
            row = dict(source=dict(queue_id=9007199254740993, id=1, type='historic',
                recorded_at='2026-10-22T12:00:00.000000Z', queue=dict(
                    installation='7', repository='owner/repo', base='main', lane='')),
                role='scope', references=[], evidence_kinds=[],
                payload_sha256='b' * 64, metadata_sha256='c' * 64)
            def write(directory, rows, m=manifest):
                (directory / 'manifest.json').write_text(json.dumps(m))
                (directory / 'selection.jsonl').write_text(''.join(json.dumps(r) + '\n' for r in rows))
            receipt, stdout = root / 'receipt', root / 'stdout'
            receipt.write_text('a' * 64 + '\n')
            stdout.write_text('output\nmanifest SHA-256: ' + 'a' * 64 + '\n')
            def check(code, reason='', extra=()):
                result = subprocess.run([sys.executable, str(SCRIPT), '--anchor', str(receipt),
                    '--replay-stdout', str(stdout), '--replay-out', 'output',
                    '--earlier', str(a), '--later', str(b), *extra], capture_output=True, text=True)
                self.assertEqual(result.returncode, code, result.stdout + result.stderr)
                self.assertIn(reason, result.stdout + result.stderr)
            write(a, [row]); write(b, [row]); check(0)
            write(b, []); check(1, 'missing source'); check(1, 'missing source', ['--require-full-coverage'])
            added = json.loads(json.dumps(row)); added['source']['id'] = 2
            write(b, [row, added]); check(1, 'in-window addition')
            added['source']['id'] = 1; added['payload_sha256'] = 'd' * 64
            write(b, [added]); check(1, 'source mismatch')
            write(b, [row, row]); check(2)
            partial = json.loads(json.dumps(manifest))
            partial['request'].update({'from': '2026-10-22T12:00:00.000000Z', 'until': '2026-10-24T00:00:00.000000Z'})
            write(b, [], partial); check(1, 'missing source')
            partial['request']['from'] = '2026-10-22T13:00:00.000000Z'
            write(b, [], partial); check(0)
            check(1, 'interval not contained', ['--require-full-coverage'])
            dependency = json.loads(json.dumps(row))
            dependency['source'].update(id=2, recorded_at='2026-10-21T12:00:00.000000Z')
            dependency['role'] = 'dependency'
            write(a, [row, dependency]); write(b, [row]); check(0)
            check(1, 'missing source', ['--require-full-coverage'])
            changed_context = json.loads(json.dumps(row)); changed_context['references'] = [{}]
            write(a, [row]); write(b, [changed_context]); check(0, 'context differs')
            for bad in [True, 1.0, 0, '1']:
                badrow = json.loads(json.dumps(row)); badrow['source']['id'] = bad
                write(b, [badrow]); check(2)
            badrow = json.loads(json.dumps(row)); badrow['source']['recorded_at'] = '2026-10-22T12:00:00Z'
            write(b, [badrow]); check(2)
            write(b, [row])
            (b / 'selection.jsonl').write_text(json.dumps(row).replace('"id": 1', '"id": 1, "id": 1') + '\n')
            check(2)
            write(a, []); disjoint = json.loads(json.dumps(manifest))
            disjoint['request'].update({'from': '2026-10-24T00:00:00.000000Z', 'until': '2026-10-25T00:00:00.000000Z'})
            empty = json.loads(json.dumps(manifest))
            empty['request']['until'] = empty['request']['from']
            write(a, [], empty)
            write(b, [], disjoint); check(0)
            check(1, 'interval not contained', ['--require-full-coverage'])
            write(a, [row]); write(b, [row])
            for malformed in ['A' * 64 + '\n', 'a' * 64, 'a' * 64 + '\n\n', 'a' * 64 + ' file\n']:
                receipt.write_text(malformed); check(2)
            receipt.write_text('d' * 64 + '\n'); check(1, 'anchor mismatch')
            receipt.write_text('a' * 64 + '\n')
            for malformed in ['wrong\nmanifest SHA-256: ' + 'a' * 64 + '\n',
                              'output\nmanifest SHA-256: ' + 'a' * 64 + '\nmanifest SHA-256: ' + 'a' * 64 + '\n']:
                stdout.write_text(malformed); check(2)


if __name__ == '__main__':
    unittest.main()
