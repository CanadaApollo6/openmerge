#!/usr/bin/env python3
"""Independent export v2 byte oracle (no Go calls).

Literal 3a payloads in journal-v2.json supply provenance inputs. Frozen v1
selection relationships supply unchanged legacy edges. V2 relationships below
are specified explicitly, not inferred from an exported v2 bundle.
"""
import copy
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1] / 'internal/audit/testdata'


def canonical(value):
    raw = json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'))
    for char in '\u0085\u2028\u2029':
        raw = raw.replace(char, '\\u%04x' % ord(char))
    return raw.encode('utf-8')


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def reference(owner, source, identity):
    return dict(kind='provenance', relation='relied_on', status='recorded',
                owner=owner, expected_type=source['type'], identity=identity,
                generation=0, run_id=0, attempt=0, source=source)


def derive(name, journal):
    events = {e['source']['id']: e for e in journal['events']}
    refs = {id: [] for id in events}
    # Frozen independent v1 relationships; rebind immutable source positions.
    for raw in (ROOT / 'approval-selection.jsonl').read_text().splitlines():
        old = json.loads(raw)
        id = old['source']['id'] * 10
        for ref in old['references']:
            ref['owner']['event_id'] *= 10
            if ref['source']:
                ref['source'] = copy.deepcopy(events[ref['source']['id'] * 10]['source'])
            refs[id].append(ref)
    owner = dict(queue_id=1, event_id=0, batch_id='', attempt_id='', change_id='', revision=0, head='', tested_sha='')
    for id in [161, 163]:
        own = dict(owner, event_id=id)
        for i, target in enumerate([161, 163]):
            request = events[id]['payload']['data']['request']
            refs[id].append(reference(own, events[target]['source'], ['inventory', request['ChangeID'], request['Generation'], str(i)]))
    for id in [171, 201]:
        data = events[id]['payload']['data']
        own = dict(owner, event_id=id, batch_id=data['batch_id'], attempt_id=data['attempt_id'])
        for relation, targets in [('decision', [170]), ('inventory', [161, 163]), ('layer', [90, 160])]:
            for target in targets:
                ref = reference(own, events[target]['source'], [relation, str(target)])
                ref['kind'] = 'provenance' if target in [161, 163] else 'settlement' if target == 170 else 'check' if target == 90 else 'eligibility'
                refs[id].append(ref)
        if id == 201:
            refs[id].append(reference(own, events[171]['source'], [data['attempt_id'], 'tested']))
    # The unextended legacy decision gains exact tested/landed companion edges.
    for stage, target in [('tested', 171), ('landed', 201)]:
        refs[170].append(reference(dict(owner, event_id=170), events[target]['source'], ['decision', '170', stage]))
    for id in refs:
        refs[id] = sorted(refs[id], key=canonical)
    request = journal['request']
    roots = {id for id, e in events.items() if request['from'] <= e['source']['recorded_at'] < request['until']}
    selected = set(roots)
    pending = list(roots)
    while pending:
        id = pending.pop()
        for ref in refs[id]:
            target = ref['source']['id'] if ref['source'] else None
            if target is not None and target not in selected:
                selected.add(target)
                pending.append(target)
    selections, envelopes = [], []
    terminal = '0' * 64
    kinds = {json.loads(l)['source']['id'] * 10: json.loads(l)['evidence_kinds'] for l in (ROOT / 'approval-selection.jsonl').read_text().splitlines()}
    for id in sorted(selected):
        event = events[id]
        source = event['source']
        role = 'scope' if id in roots else 'dependency'
        kind = kinds.get(id, ['provenance'])
        meta = dict(source=source, role=role, references=refs[id], evidence_kinds=kind)
        selections.append(dict(meta, payload_sha256=sha(canonical(event['payload'])), metadata_sha256=sha(canonical(meta))))
        env = dict(meta, kind='source', payload=event['payload'], previous_sha256=terminal)
        envelopes.append(env)
        terminal = sha(canonical(env))
    dependencies = sorted(selected - roots)
    boundary = [] if not dependencies else [dict(queue_id=1, queue=events[dependencies[0]]['source']['queue'], first=dependencies[0], last=dependencies[-1], count=len(dependencies))]
    files = {'events.jsonl': envelopes, 'selection.jsonl': selections, 'watermarks.jsonl': journal['watermarks'], 'dependency_boundaries.jsonl': boundary}
    manifest = dict(version=2, request=request, count=len(selected), terminal_sha256=terminal, manifest_sha256='')
    for field, file in [('events', 'events.jsonl'), ('selection', 'selection.jsonl'), ('watermarks', 'watermarks.jsonl'), ('dependency_boundaries', 'dependency_boundaries.jsonl')]:
        raw = b''.join(canonical(row) + b'\n' for row in files[file])
        (ROOT / f'{name}-{file}').write_bytes(raw)
        manifest[field] = file
        manifest[field + '_sha256'] = sha(raw)
    manifest['manifest_sha256'] = sha(canonical(manifest))
    (ROOT / f'{name}-manifest.json').write_bytes(canonical(manifest) + b'\n')


if __name__ == '__main__':
    for name, journal in json.loads((ROOT / 'journal-v2.json').read_text()).items():
        derive(name, journal)
