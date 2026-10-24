#!/usr/bin/env python3
"""Owned scratch lifecycle, restore and exact operator-role acceptance (no forge I/O)."""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import uuid
from urllib.parse import urlsplit, urlunsplit
sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url

FILES = ('manifest.json', 'events.jsonl', 'selection.jsonl', 'watermarks.jsonl',
         'dependency_boundaries.jsonl')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--test-binary', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    base = scratch_url(os.environ['OPENMERGE_TEST_DATABASE_URL'])
    if urlsplit(base).port != 54329:
        raise ValueError('this acceptance requires localhost:54329')
    out = args.out.resolve()
    if not out.is_relative_to(Path.home() / '.cache'):
        raise ValueError('artifacts must be under ~/.cache')
    out.mkdir(mode=0o700, parents=True, exist_ok=False)
    names = ['om_9c2c_' + uuid.uuid4().hex for _ in range(3)]
    urls = [scratch_url(base, name) for name in names]
    # These names are generated here, never accepted from a caller.
    role = 'om_10b_' + uuid.uuid4().hex
    databases, roles, commands = [], [], []

    def run(argv, stdin=None, expected=0, env=None, cwd=None, raw=False):
        result = subprocess.run(list(map(str, argv)), input=stdin, text=True,
                                capture_output=True, timeout=180, env=env, cwd=cwd)
        # DSNs carry local credentials: retain redacted commands, raw outputs.
        command = [str(a) if not str(a).startswith('postgres') else '<scratch-dsn>' for a in argv]
        commands.append({'argv': command, 'stdin': stdin, 'exit': result.returncode,
                         'stdout': result.stdout, 'stderr': result.stderr})
        if result.returncode != expected:
            raise RuntimeError(f'{command[0]} exit {result.returncode}, expected {expected}; see commands.json')
        return result.stdout if raw else result.stdout.strip()

    def sql(url, text, expected=0):
        return run(['psql', '-X', '-qAt', '-v', 'ON_ERROR_STOP=1', url], text, expected)

    def export(url, name, manifest=None, expected=0):
        argv = [args.binary.resolve(), 'audit', 'export', '--out', out / name]
        request = json.loads((out / 'final/manifest.json').read_text())['request']
        argv += ['--manifest', manifest] if manifest else ['--repo', 'acme/retention', '--installation', '7', '--from', request['from'], '--until', request['until']]
        stdout = run(argv, expected=expected, env=dict(os.environ, OPENMERGE_DATABASE_URL=url), raw=True)
        if expected == 0 and not re.fullmatch(re.escape(str(out / name)) + r"\nmanifest SHA-256: [0-9a-f]{64}\n", stdout):
            raise RuntimeError("invalid export stdout")
        return out / name

    def equal(left, right):
        for name in FILES:
            if (left / name).read_bytes() != (right / name).read_bytes():
                raise RuntimeError('export bytes differ: ' + name)

    inventory = "SELECT jsonb_build_object('queues',(SELECT jsonb_agg(to_jsonb(q) ORDER BY id) FROM queues q),'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e))::text"
    summary = {'databases': names, 'role': role, 'replay_os_identity': os.getuid()}
    try:
        for name in names:
            sql(base, 'CREATE DATABASE "' + name + '"')
            databases.append(name)
        env = dict(os.environ, OPENMERGE_10B_DATABASE_URL=urls[0], OPENMERGE_10B_OUT=str(out))
        run([args.test_binary.resolve(), '-test.run=^TestRetentionLifecycle$', '-test.v'], env=env, cwd=Path(__file__).resolve().parents[1] / 'cmd/openmerge')
        # Pin linked families so an accidentally empty journey cannot pass.
        counts = {}
        for event in json.loads((out / 'inventory-final.json').read_text())['events']:
            counts[event['type']] = counts.get(event['type'], 0) + 1
        for kind in ('landing_decision_recorded', 'approval_eligibility_observed',
                     'review_stance_observed', 'approval_receipt_recorded', 'change_revision_observed', 'change_classified',
                     'change_accountability_observed', 'policy_authority_applied',
                     'write_restriction_verified', 'batch_started', 'test_ref_built',
                     'landing_checks_recorded', 'checks_completed', 'landing_started', 'landed',
                     'lifecycle_changed'):
            if not counts.get(kind):
                raise RuntimeError('missing lifecycle family: ' + kind)
        receipts = [e['payload']['data']['accepted'] for e in json.loads((out / 'inventory-final.json').read_text())['events'] if e['type'] == 'approval_receipt_recorded']
        if set(receipts) != {False, True}:
            raise RuntimeError('accepted/rejected receipts absent')
        summary['event_counts'] = counts
        # Freeze all writers before backup; the Go test closed server and store.
        sql(base, f'CREATE ROLE "{role}" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS')
        roles.append(role)
        sql(urls[0], f'REVOKE CREATE,TEMP ON DATABASE "{names[0]}" FROM PUBLIC; REVOKE CREATE ON SCHEMA public FROM PUBLIC; GRANT CONNECT ON DATABASE "{names[0]}" TO "{role}"; GRANT USAGE ON SCHEMA public TO "{role}"; GRANT SELECT ON events,queues TO "{role}"')
        dump = out / 'journal.dump'
        run(['pg_dump', '-Fc', '--file=' + str(dump), urls[0]])
        # Role exists before restore so ACLs restore with --exit-on-error.
        run(['pg_restore', '--exit-on-error', '--dbname', urls[1], dump])
        if sql(urls[0], inventory) != sql(urls[1], inventory):
            raise RuntimeError('restore changed complete inventory')
        restored = export(urls[1], 'restored')
        equal(out / 'final', restored)
        equal(out / 'before', export(urls[1], 'restored-before', out / 'before/manifest.json'))
        equal(restored, export(urls[1], 'restored-replay', restored / 'manifest.json'))
        # Mirror TestAuditBareRemote's object/parent read-back on both exports.
        oracle = json.loads((out / 'git-oracle.json').read_text())
        for bundle in (out / 'final', restored):
            seen, objects, confirmed, batches = set(), set(), set(), {}
            for line in (bundle / 'events.jsonl').read_text().splitlines():
                event = json.loads(line)
                kind, data = event['source']['type'], event['payload']['data']
                for field in ('tested_sha', 'landed_sha', 'new_base_sha'):
                    if data.get(field):
                        objects.add(data[field])
                        if 'batch_id' in data:
                            batches.setdefault(data['batch_id'], set()).add(data[field])
                        if field == 'new_base_sha':
                            confirmed.add(data[field])
                        seen.add(field)
                if kind == 'outside_push_recorded':
                    sha = data['new_sha']
                    if oracle['parents'].get(sha) != data['previous_sha']:
                        raise RuntimeError('outside move differs from Git parent')
                    commits = run(['git', '--git-dir=' + str(out / 'remote.git'), 'rev-list', sha, '^' + data['previous_sha']])
                    if commits != sha:
                        raise RuntimeError('outside move differs from Git history')
                    seen.add('outside')
            if confirmed != set(oracle['landings']) or any(len(values) != 1 for values in batches.values()):
                raise RuntimeError('tested/intended/confirmed SHA differs')
            for sha in objects:
                obj = run(['git', '--git-dir=' + str(out / 'remote.git'), 'cat-file', '-p', sha])
                if not any('\n'.join('parent ' + p for p in parents.split()) in obj for parents in oracle['landings'].values()):
                    raise RuntimeError('landing graph differs')
            if seen != {'tested_sha', 'landed_sha', 'new_base_sha', 'outside'}:
                raise RuntimeError('missing Git facts')
        p = urlsplit(urls[0])
        reader = urlunsplit((p.scheme, role + '@localhost:54329', p.path, p.query, ''))
        # Guard routing again after changing the login, before any subprocess.
        reader = scratch_url(reader, names[0])
        equal(out / 'final', export(reader, 'reader'))
        equal(out / 'before', export(reader, 'reader-replay', out / 'before/manifest.json'))
        if sql(reader, "SELECT has_database_privilege(current_user,current_database(),'CREATE'),has_database_privilege(current_user,current_database(),'TEMP'),has_schema_privilege(current_user,'public','CREATE')") != 'f|f|f':
            raise RuntimeError('operator has database/schema write privileges')
        grants = sql(reader, "SELECT table_name,privilege_type FROM information_schema.role_table_grants WHERE grantee=current_user ORDER BY 1,2")
        if grants != 'events|SELECT\nqueues|SELECT':
            raise RuntimeError('operator table grants differ')
        for probe in ('INSERT INTO events(queue_id,type,payload) VALUES(1,\'base_moved\',\'{}\')',
                      'UPDATE events SET type=type', 'DELETE FROM events',
                      'CREATE TABLE public.forbidden(id int)', 'CREATE TEMP TABLE forbidden(id int)'):
            sql(reader, probe, expected=3)
        for table in ('events', 'queues'):
            sql(urls[0], f'REVOKE SELECT ON {table} FROM "{role}"')
            export(reader, 'missing-' + table, expected=1)
            sql(urls[0], f'GRANT SELECT ON {table} TO "{role}"')
        # Required dependency removal only in an isolated restored clone.
        run(['pg_restore', '--exit-on-error', '--dbname', urls[2], dump])
        sql(urls[2], "DELETE FROM events WHERE id=(SELECT min(id) FROM events WHERE type='approval_eligibility_observed')")
        export(urls[2], 'missing-dependency', out / 'before/manifest.json', expected=1)
        summary.update(verdict='PASS', dump_sha256=hashlib.sha256(dump.read_bytes()).hexdigest(),
                       final_hashes={n: hashlib.sha256((restored / n).read_bytes()).hexdigest() for n in FILES},
                       inventory_sha256=hashlib.sha256(sql(urls[1], inventory).encode()).hexdigest(),
                       postgres=sql(base, 'SELECT version()'), tools=run(['pg_dump', '--version']))
    finally:
        # ACLs in restored/cloned DBs must disappear before the cluster-global role.
        for name in reversed(databases):
            sql(base, 'DROP DATABASE "' + name + '"')
        for owned in reversed(roles):
            sql(base, 'DROP ROLE "' + owned + '"')
        if sql(base, "SELECT count(*) FROM pg_database WHERE datname IN ('" + "','".join(databases) + "')") != '0':
            raise RuntimeError('owned databases remain')
        if sql(base, f"SELECT count(*) FROM pg_roles WHERE rolname='{role}'") != '0':
            raise RuntimeError('owned role remains')
        summary['scratch_resources_removed'] = True
        (out / 'commands.json').write_text(json.dumps(commands, indent=2) + '\n')
        (out / 'summary.json').write_text(json.dumps(summary, indent=2) + '\n')
    print(json.dumps(summary, indent=2))


if __name__ == '__main__':
    main()
