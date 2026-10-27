"""Contract cases; invoked by Go so the existing CI runs them."""
import json
import hashlib
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
                raw = ''.join(json.dumps(r) + '\n' for r in rows).encode()
                (directory / 'manifest.json').write_text(json.dumps({**m, 'selection_sha256': hashlib.sha256(raw).hexdigest()}))
                (directory / 'selection.jsonl').write_bytes(raw)
            receipt, stdout = root / 'receipt', root / 'stdout'
            receipt.write_text('a' * 64 + '\n')
            stdout.write_text('output\nmanifest SHA-256: ' + 'a' * 64 + '\n')
            def check(code, reason='', extra=(), out='output', later=True, input_text=''):
                result = subprocess.run([sys.executable, str(SCRIPT), '--anchor', str(receipt),
                    '--replay-stdout', str(stdout), '--replay-out', out,
                    '--earlier', str(a), *(['--later', str(b)] if later else []), *extra], capture_output=True, text=True, input=input_text)
                self.assertEqual(result.returncode, code, result.stdout + result.stderr)
                self.assertIn(reason, result.stdout + result.stderr)
                self.assertEqual(result.stderr, 'malformed or unreadable input\n' if code == 2 else '')
                if code == 2: self.assertEqual(result.stdout, '')
                self.assertEqual('anchor and overlap match' in result.stdout, code == 0)
                if code == 0 and not reason: self.assertEqual(result.stdout, 'anchor and overlap match\n')
                return result
            write(a, [row]); write(b, [row]); check(0)
            check(0, extra=['--require-full-coverage'])
            # Replay's manifest/stdout remain frozen; erase A after replay while B is empty.
            write(b, []); (a / 'selection.jsonl').write_bytes(b''); check(2)
            write(a, [row]); write(b, [row])
            for directory in [a, b]:
                saved = root / 'saved'
                directory.rename(saved); directory.symlink_to(saved, target_is_directory=True)
                check(2); directory.unlink(); saved.rename(directory)
                target = directory / 'selection.jsonl'
                target.write_bytes(target.read_bytes().replace(b'\n', b'\r\n'))
                # Rebind hash so CRLF rejection is independently observable.
                m = json.loads((directory / 'manifest.json').read_text())
                m['selection_sha256'] = hashlib.sha256(target.read_bytes()).hexdigest()
                (directory / 'manifest.json').write_text(json.dumps(m)); check(2)
                write(directory, [row])
            target = b / 'selection.jsonl'
            target.write_bytes(target.read_bytes().replace(b'b' * 64, b'd' * 64)); check(2)
            write(b, [row])
            for value in ['z' * 64, 'A' * 64, 'a' * 65, None]:
                m = json.loads((b / 'manifest.json').read_text()); m['selection_sha256'] = value
                (b / 'manifest.json').write_text(json.dumps(m)); check(2)
            write(b, [row])  # Full-width equality: flip only the final hex digit.
            m = json.loads((b / 'manifest.json').read_text()); good = m['selection_sha256']
            m['selection_sha256'] = good[:-1] + ('0' if good[-1] != '0' else '1')
            (b / 'manifest.json').write_text(json.dumps(m)); check(2)
            write(b, [row])
            global_findings = json.loads(json.dumps(manifest))
            global_findings['request'].update(installation='8', **{'from': '2026-10-22T13:00:00.000000Z'})
            write(b, [], global_findings)
            receipt.write_text('d' * 64 + '\n')
            self.assertEqual(check(1, extra=['--require-full-coverage']).stdout,
                'anchor mismatch\nmissing source: full coverage interval not contained\n'
                'source mismatch: bundle identity\n'
                f"missing source queue_id={row['source']['queue_id']} id=1\n")
            receipt.write_text('a' * 64 + '\n'); write(b, [row])

            later_start = json.loads(json.dumps(manifest)); later_start['request']['from'] = '2026-10-22T13:00:00.000000Z'
            context = json.loads(json.dumps(row)); context['role'] = 'dependency'
            write(b, [context], later_start); check(0, 'context differs')
            context.update(role='scope', evidence_kinds=['check']); write(b, [context]); check(0, 'context differs')
            ordered = json.loads(json.dumps(row)); ordered['references'] = [{'source': ordered['source']}]
            reordered = json.loads(json.dumps(ordered))
            for source in [reordered['source'], reordered['references'][0]['source']]: source['queue'] = dict(reversed(list(source['queue'].items())))
            write(a, [ordered]); write(b, [reordered]); check(0); write(a, [row]); write(b, [row])
            mismatch = json.loads(json.dumps(row)); mismatch['payload_sha256'] = 'd' * 64
            missing = json.loads(json.dumps(row)); missing['source']['id'] = 3
            addition = json.loads(json.dumps(row)); addition['source']['id'] = 2
            low = json.loads(json.dumps(mismatch)); low['source'].update(queue_id=2, id=9)
            low_original = json.loads(json.dumps(low)); low_original['payload_sha256'] = row['payload_sha256']
            write(a, [missing, row, low_original]); write(b, [addition, mismatch, low])
            expected = ['source mismatch queue_id=2 id=9', f"source mismatch queue_id={row['source']['queue_id']} id=1", f"in-window addition queue_id={row['source']['queue_id']} id=2", f"missing source queue_id={row['source']['queue_id']} id=3"]
            self.assertEqual(check(1).stdout, '\n'.join(expected) + '\n'); write(a, [row]); write(b, [row])
            for out in ['o.y', 'a(b', '[x]+']:
                stdout.write_text(out + '\nmanifest SHA-256: ' + 'a' * 64 + '\n'); check(0, out=out)
            stdout.write_text('oXy\nmanifest SHA-256: ' + 'a' * 64 + '\n'); check(2, out='o.y')
            stdout.write_text('output\nmanifest SHA-256: ' + 'a' * 64 + '\n'); check(2, extra=['--unknown'])

            largest = json.loads(json.dumps(row)); largest['source'].update(queue_id=2**63-1, id=2**63-1)
            write(a, [largest]); write(b, [largest]); check(0); write(a, [row]); write(b, [row])
            check(0, extra=['--anchor', '-'], input_text='a' * 64 + '\n')
            check(2, extra=['--anchor', '-'], input_text='a' * 64 + '\nX')
            check(2, extra=['--anchor', '-'], input_text='a' * 65 + '\n')
            check(0, extra=['--replay-stdout', '-'], input_text=stdout.read_text())
            for out in ['o\nx', 'o\rx', '']:
                stdout.write_text(out + '\nmanifest SHA-256: ' + 'a' * 64 + '\n'); check(2, out=out)
            stdout.write_text('output\nmanifest SHA-256: ' + 'a' * 64 + '\n')
            other = json.loads(json.dumps(row)); other['source']['queue_id'] += 1; other['source']['queue']['lane'] = 'other'
            write(a, [row, other]); write(b, [row]); check(1, 'missing source')
            write(a, [row]); write(b, [row, other]); check(1, 'in-window addition'); write(b, [row])
            other['source']['queue_id'] = row['source']['queue_id']; other['source']['id'] = 2; other['source']['queue'] = row['source']['queue']
            write(b, [other]); check(1, 'missing source'); check(1, 'in-window addition'); write(b, [row])

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
            for field, value in [('installation', '8'), ('repository', 'other/repo'), ('repository', 'z/repo'), ('base', '\ud800')]:
                bad = json.loads(json.dumps(row)); bad['source']['queue'][field] = value
                write(b, [bad]); check(2)
            for path, value in [(('source', 'type'), ''), (('source', 'type'), '\ud800'), (('role',), 'other'), (('metadata_sha256',), 'bad'), (('payload_sha256',), 'b' * 65), (('payload_sha256',), 'B' * 64)]:
                bad = json.loads(json.dumps(row)); target = bad
                for key in path[:-1]: target = target[key]
                target[path[-1]] = value; write(b, [bad]); check(2)
            bad = json.loads(json.dumps(row)); bad.update(role='other')
            bad['source']['recorded_at'] = '2026-10-21T00:00:00.000000Z'
            write(b, [bad]); check(2); bad.update(role='dependency', payload_sha256='zz'); write(b, [bad]); check(2)
            for path, value in [(('source', 'recorded_at'), '2026-13-22T12:00:00.000000Z'), (('source', 'recorded_at'), '2026-10-22T12:00:00.00000Z'), (('source', 'extra'), 1), (('source', 'queue', 'extra'), 'extra'), (('references',), {}), (('evidence_kinds',), {}), (('references',), [1.5]), (('evidence_kinds',), [float('nan')])]:
                bad = json.loads(json.dumps(row)); target = bad
                for key in path[:-1]: target = target[key]
                target[path[-1]] = value; write(b, [bad]); check(2)
            for at in ['2026-13-22T12:00:00.000000Z', '2026-1-22T12:00:00.000000Z', '2026-10-2T12:00:00.000000Z',
                       '2026-10-21T1:00:00.000000Z', '2026-10-21T12:0:00.000000Z', '2026-10-21T12:00:0.000000Z']:
                bad = json.loads(json.dumps(row)); bad['source']['recorded_at'] = at; bad['role'] = 'dependency'; write(b, [bad]); check(2)
            for field, value in [('version', True), ('version', 3), ('manifest_sha256', 'zz'), ('manifest_sha256', 'a' * 65), ('manifest_sha256', 'A' * 64)]:
                bad = json.loads(json.dumps(manifest)); bad[field] = value; write(b, [], bad); check(2)
            for field, value in [('repository', ''), ('installation', ''), ('repository', 7), ('installation', 7), ('from', '2026-10-24T00:00:00.000000Z')]:
                bad = json.loads(json.dumps(manifest)); bad['request'][field] = value
                write(b, [], bad); check(2)
            for field, value in [('queue', {**row['source']['queue'], 'base': 'other'}), ('type', 'other'), ('recorded_at', '2026-10-22T12:01:00.000000Z')]:
                bad = json.loads(json.dumps(row)); bad['source'][field] = value
                write(b, [bad]); check(1, 'source mismatch')
            for name, rows, needle, bad in [('manifest.json', [], b'owner/repo', b'owner/\xffrepo'), ('selection.jsonl', [row], b'"lane": ""', b'"lane": "\xff"')]:
                write(b, [row]); (b / name).write_bytes(b'\xff\n'); check(2)
                write(b, rows); target = b / name; raw = target.read_bytes()
                assert raw.count(needle) == 1
                target.write_bytes(raw.replace(needle, bad)); check(2)
            write(b, [row]); selection = b / 'selection.jsonl'
            selection.write_bytes(selection.read_bytes()[:-1]); check(2)
            oversized = json.loads(json.dumps(row)); oversized['source']['type'] = 'x' * (4 * 1024 * 1024)
            write(b, [oversized]); check(2)
            oversized = json.loads(json.dumps(manifest)); oversized['request']['repository'] = 'x' * (4 * 1024 * 1024)
            write(b, [], oversized); check(2); write(b, [row])
            target = b / 'manifest.json'; raw = target.read_bytes()
            target.write_bytes(raw + b' ' * (4 * 1024 * 1024 - len(raw))); check(0)
            target.write_bytes(target.read_bytes() + b' '); check(2); write(b, [row])
            maximum = json.loads(json.dumps(row)); maximum['references'] = ['']
            maximum['references'] = ['x' * (4 * 1024 * 1024 - len(json.dumps(maximum)))]
            write(b, [maximum]); check(0, 'context differs')
            maximum['references'][0] += 'x'; write(b, [maximum]); check(2); write(b, [row])


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
            for malformed in ['output\nmanifest SHA-256: ' + 'a' * 64, 'wrong\nmanifest SHA-256: ' + 'a' * 64 + '\n',
                              'output\nmanifest SHA-256: ' + 'a' * 64 + '\nmanifest SHA-256: ' + 'a' * 64 + '\n',
                              'output\nmanifest SHA-256: ' + 'A' * 64 + '\n', 'output\nmanifest SHA-256: ' + 'a' * 65 + '\n']:
                stdout.write_text(malformed); check(2)


if __name__ == '__main__':
    unittest.main()
