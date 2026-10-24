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
            def check(code, reason='', extra=(), out='output', later=True):
                result = subprocess.run([sys.executable, str(SCRIPT), '--anchor', str(receipt),
                    '--replay-stdout', str(stdout), '--replay-out', out,
                    '--earlier', str(a), *(['--later', str(b)] if later else []), *extra], capture_output=True, text=True, input='')
                self.assertEqual(result.returncode, code, result.stdout + result.stderr)
                self.assertIn(reason, result.stdout + result.stderr)
            write(a, [row]); write(b, [row]); check(0)
            check(0, extra=['--require-full-coverage'])
            check(2, extra=['--require-full-coverage'], later=False)
            extended = json.loads(json.dumps(manifest)); extended['request']['until'] = '2026-10-24T00:00:00.000000Z'
            at_end = json.loads(json.dumps(row)); at_end['source'].update(id=2, recorded_at=manifest['request']['until'])
            write(b, [row, at_end], extended); check(0); write(b, [row])

            for separator in '\v\f\x1c\x1d\x1e\x85\u2028\u2029':
                crafted = 'output' + separator + 'manifest SHA-256: ' + 'a' * 64 + separator + 'tail'
                stdout.write_text(crafted + '\nmanifest SHA-256: ' + 'd' * 64 + '\n')
                check(1, 'anchor mismatch', out=crafted)
                benign = 'output' + separator + 'tail'
                stdout.write_text(benign + '\nmanifest SHA-256: ' + 'a' * 64 + '\n')
                check(0, out=benign)
            stdout.write_text('output\nmanifest SHA-256: ' + 'd' * 64 + '\n')
            check(1, 'anchor mismatch')  # Anchor equals manifest only.
            receipt.write_text('d' * 64 + '\n'); check(1, 'anchor mismatch')  # Equals stdout only.
            receipt.write_text('a' * 64 + '\n'); stdout.write_text('output\nmanifest SHA-256: ' + 'a' * 64 + '\n')
            check(2, extra=['--anchor', '-', '--replay-stdout', '-'])
            malformed = json.loads(json.dumps(row)); malformed['source']['queue'] = list(row['source']['queue'])
            write(b, [malformed]); check(2); write(b, [row])
            for file in ['manifest.json', 'selection.jsonl']:
                target = b / file; saved = root / file; target.rename(saved); target.symlink_to(saved)
                check(2); target.unlink(); saved.rename(target)
            tail = json.loads(json.dumps(manifest)); tail['request']['until'] = '2026-10-22T13:00:00.000000Z'
            write(b, [row], tail); check(1, 'interval not contained', ['--require-full-coverage'])
            tail['request']['until'] = row['source']['recorded_at']
            write(b, [], tail); check(0)  # A source exactly at common until is not required.
            boundary = json.loads(json.dumps(row)); boundary['source']['recorded_at'] = manifest['request']['from']
            write(a, []); write(b, [boundary]); check(1, 'in-window addition')
            boundary['role'] = 'dependency'; write(b, [boundary]); check(2)
            boundary['role'] = 'scope'; boundary['source']['recorded_at'] = manifest['request']['until']
            write(b, [boundary]); check(2)
            identity = json.loads(json.dumps(manifest)); identity['request']['installation'] = '8'
            write(b, [], identity); check(1, 'bundle identity')
            identity['request'].update(installation='7', repository='other/repo')
            write(b, [], identity); check(1, 'bundle identity'); write(a, [row]); write(b, [row])

            write(b, []); check(1, 'missing source'); check(1, 'missing source', ['--require-full-coverage'])
            added = json.loads(json.dumps(row)); added['source']['id'] = 2
            write(b, [row, added]); check(1, 'in-window addition')
            added['source']['id'] = 1; added['payload_sha256'] = 'd' * 64
            write(b, [added]); check(1, 'source mismatch')
            write(b, [row, row]); check(2)
            for field, value in [('installation', '8'), ('repository', 'other/repo'), ('base', '\ud800')]:
                bad = json.loads(json.dumps(row)); bad['source']['queue'][field] = value
                write(b, [bad]); check(2)
            for path, value in [(('source', 'type'), ''), (('source', 'type'), '\ud800'), (('role',), 'other'), (('metadata_sha256',), 'bad')]:
                bad = json.loads(json.dumps(row)); target = bad
                for key in path[:-1]: target = target[key]
                target[path[-1]] = value; write(b, [bad]); check(2)
            for field, value in [('repository', ''), ('from', '2026-10-24T00:00:00.000000Z')]:
                bad = json.loads(json.dumps(manifest)); bad['request'][field] = value
                write(b, [], bad); check(2)
            for field, value in [('queue', {**row['source']['queue'], 'base': 'other'}), ('type', 'other'), ('recorded_at', '2026-10-22T12:01:00.000000Z')]:
                bad = json.loads(json.dumps(row)); bad['source'][field] = value
                write(b, [bad]); check(1, 'source mismatch')
            write(b, [row]); selection = b / 'selection.jsonl'
            selection.write_bytes(selection.read_bytes()[:-1]); check(2)
            oversized = json.loads(json.dumps(row)); oversized['source']['type'] = 'x' * (4 * 1024 * 1024)
            write(b, [oversized]); check(2)
            oversized = json.loads(json.dumps(manifest)); oversized['request']['repository'] = 'x' * (4 * 1024 * 1024)
            write(b, [], oversized); check(2); write(b, [row])

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
