#!/usr/bin/env python3
"""Offline comparison of already replay-validated, privately frozen v1 bundles."""
import argparse
import datetime
import json
import re
import sqlite3
import sys
import tempfile
from pathlib import Path

LIMIT = 4 * 1024 * 1024
HEX = re.compile(r"[0-9a-f]{64}")
TIME = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z")


def require(ok):
    if not ok:
        raise ValueError()


def pairs(items):
    result = {}
    for key, value in items:
        require(key not in result)
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=pairs,
                      parse_float=lambda _: (_ for _ in ()).throw(ValueError()),
                      parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))


def integer(value):
    require(type(value) is int and 0 < value <= 9223372036854775807)
    return value


def stamp(value):
    require(isinstance(value, str) and TIME.fullmatch(value))
    datetime.datetime.strptime(value, '%Y-%m-%dT%H:%M:%S.%fZ')
    return value


def digest(value):
    require(isinstance(value, str) and HEX.fullmatch(value))
    return value


def read(path, limit=LIMIT):
    if str(path) == '-':
        raw = sys.stdin.buffer.read(limit + 1)
    else:
        require(not path.is_symlink() and path.is_file())
        with path.open('rb') as stream:
            raw = stream.read(limit + 1)
    require(len(raw) <= limit)
    return raw.decode('utf-8')


def load(db, label, directory):
    manifest = decode(read(directory / 'manifest.json'))
    require(type(manifest['version']) is int and manifest['version'] == 1)
    digest(manifest['manifest_sha256'])
    request = manifest['request']
    require(isinstance(request['repository'], str) and request['repository'])
    require(isinstance(request['installation'], str) and request['installation'])
    start, end = stamp(request['from']), stamp(request['until'])
    require(start <= end)
    path = directory / 'selection.jsonl'
    require(not path.is_symlink() and path.is_file())
    with path.open('rb') as stream:
        while raw := stream.readline(LIMIT + 1):
            require(len(raw) <= LIMIT + 1 and raw.endswith(b'\n'))
            row = decode(raw.decode('utf-8'))
            source = row['source']
            require(isinstance(source, dict) and set(source) == {'queue_id', 'id', 'type', 'recorded_at', 'queue'})
            qid, sid = integer(source['queue_id']), integer(source['id'])
            queue = source['queue']
            require(isinstance(queue, dict) and set(queue) == {'installation', 'repository', 'base', 'lane'})
            require(all(isinstance(v, str) for v in queue.values()))
            for value in [source['type'], *queue.values()]:
                require(isinstance(value, str))
                value.encode('utf-8')
            require(queue['installation'] == request['installation'] and
                    queue['repository'] == request['repository'])
            require(isinstance(source['type'], str) and source['type'])
            at = stamp(source['recorded_at'])
            require(row['role'] in ('scope', 'dependency'))
            require((start <= at < end) == (row['role'] == 'scope'))
            digest(row['metadata_sha256'])
            facts = json.dumps([queue, source['type'], at, digest(row['payload_sha256'])], sort_keys=True)
            context = json.dumps([row['role'], row['references'], row['evidence_kinds']], sort_keys=True)
            require(isinstance(row['references'], list) and isinstance(row['evidence_kinds'], list))
            db.execute('INSERT INTO sources VALUES(?,?,?,?,?,?,?)',
                       (label, qid, sid, at, row['role'], facts, context))
    return manifest, start, end


class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, "malformed or unreadable input\n")


def main():
    parser = Parser(description=__doc__)
    parser.add_argument('--anchor', required=True, type=Path, help='strict hex receipt; - reads stdin')
    parser.add_argument('--replay-stdout', required=True, type=Path)
    parser.add_argument('--replay-out', required=True, help='exact --out value used by successful replay')
    parser.add_argument('--earlier', required=True, type=Path)
    parser.add_argument('--later', type=Path)
    parser.add_argument('--require-full-coverage', action='store_true')
    args = parser.parse_args()
    try:
        require(not args.require_full_coverage or args.later is not None)
        require(sum(str(p) == '-' for p in (args.anchor, args.replay_stdout)) <= 1)
        anchor = read(args.anchor, 65)
        require(re.fullmatch(r'[0-9a-f]{64}\n', anchor))
        stdout = read(args.replay_stdout)
        require('\n' not in args.replay_out and '\r' not in args.replay_out and args.replay_out)
        match = re.fullmatch(re.escape(args.replay_out) + r'\nmanifest SHA-256: ([0-9a-f]{64})\n', stdout)
        require(match is not None)
        replay_digest = match.group(1)
        with tempfile.TemporaryDirectory(prefix='openmerge-anchor-') as temporary:
            with sqlite3.connect(str(Path(temporary) / 'join.sqlite')) as db:
                db.execute('CREATE TABLE sources(label TEXT,q INTEGER,id INTEGER,at TEXT,role TEXT,facts TEXT,context TEXT,PRIMARY KEY(label,q,id))')
                a, start, end = load(db, 'a', args.earlier)
                failed = False
                db.execute('CREATE TABLE findings(q INTEGER,id INTEGER,message TEXT)')
                def finding(reason, identity='', qid=0, sid=0):
                    nonlocal failed
                    failed = True
                    db.execute('INSERT INTO findings VALUES(?,?,?)', (qid, sid, reason + (' ' + identity if identity else '')))
                if anchor[:-1] != replay_digest or anchor[:-1] != a['manifest_sha256']:
                    finding('anchor mismatch')
                if args.later:
                    b, bstart, bend = load(db, 'b', args.later)
                    if (a['version'], a['request']['repository'], a['request']['installation']) != (b['version'], b['request']['repository'], b['request']['installation']):
                        finding('source mismatch: bundle identity')
                    if args.require_full_coverage and not (bstart <= start and end <= bend):
                        finding('missing source: full coverage interval not contained')
                    for qid, sid, at, role, facts, context, bfacts, bcontext in db.execute("SELECT a.q,a.id,a.at,a.role,a.facts,a.context,b.facts,b.context FROM sources a LEFT JOIN sources b ON b.label='b' AND a.q=b.q AND a.id=b.id WHERE a.label='a' ORDER BY a.q,a.id"):
                        identity = f'queue_id={qid} id={sid}'
                        if bfacts is None:
                            if args.require_full_coverage or role == 'scope' and max(start, bstart) <= at < min(end, bend):
                                finding('missing source', identity, qid, sid)
                        elif facts != bfacts:
                            finding('source mismatch', identity, qid, sid)
                        elif context != bcontext:
                            db.execute('INSERT INTO findings VALUES(?,?,?)', (qid, sid, 'context differs ' + identity))
                    for qid, sid in db.execute("SELECT b.q,b.id FROM sources b LEFT JOIN sources a ON a.label='a' AND a.q=b.q AND a.id=b.id WHERE b.label='b' AND a.id IS NULL AND b.at>=? AND b.at<? ORDER BY b.q,b.id", (start, end)):
                        finding('in-window addition', f'queue_id={qid} id={sid}', qid, sid)
                for message, in db.execute('SELECT message FROM findings ORDER BY q,id,message'):
                    print(message)
                if not failed:
                    print('anchor and overlap match')
                return int(failed)
    except Exception:  # Unexpected script bugs also map to sanitized exit 2.
        print('malformed or unreadable input', file=sys.stderr)
        return 2


if __name__ == '__main__':
    sys.exit(main())
