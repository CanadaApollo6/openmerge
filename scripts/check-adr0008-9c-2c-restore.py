#!/usr/bin/env python3
"""Exercise pre-emission dump/restore and authority downgrade on owned scratch DBs."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlsplit, urlunsplit
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    base = os.environ["OPENMERGE_TEST_DATABASE_URL"]
    parsed = urlsplit(base)
    if parsed.hostname != "localhost" or parsed.port != 54329:
        raise RuntimeError("only shared test Postgres localhost:54329 is permitted")
    out = args.out.resolve()
    if not out.is_relative_to(Path.home() / ".cache"):
        raise RuntimeError("scratch artifacts must be under ~/.cache")
    out.mkdir(parents=True, exist_ok=True)
    binary = str(args.binary.resolve())
    names = ["om_9c2c_" + uuid.uuid4().hex for _ in range(2)]
    urls = [urlunsplit(parsed._replace(path="/" + name)) for name in names]
    created = []

    def run(argv, *, stdin=None, expected=0):
        result = subprocess.run(argv, input=stdin, text=True, capture_output=True, timeout=120)
        if result.returncode != expected:
            # Deliberately do not print DSNs or server diagnostic bodies.
            raise RuntimeError(f"scratch {Path(argv[0]).name} exited {result.returncode}, expected {expected}")
        return result.stdout

    def sql(url, text):
        return run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", url], stdin=text).strip()

    def identity(url):
        return run([binary, "setup", "rulesets", "--print-database-identity", "--database-url", url]).strip()

    summary = {"scope": "isolated synthetic DB restore; no live GitHub refs", "databases": names}
    try:
        for name in names:
            sql(base, 'CREATE DATABASE "' + name + '"')
            created.append(name)
        # Existing schema only: no new migration or production table is introduced.
        sql(urls[0], "CREATE TABLE schema_migrations(version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());")
        for path in sorted((root / "internal/store/migrations").glob("*.sql")):
            sql(urls[0], path.read_text() + "\nINSERT INTO schema_migrations(version) VALUES ('migrations/" + path.name + "');")
        event = json.dumps({"version": 1, "type": "base_moved", "data": {"sha": "before-backup"}})
        sql(urls[0], "INSERT INTO queues(installation,repository,base) VALUES('7','fixture/repo','main'); INSERT INTO events(queue_id,type,payload) VALUES(1,'base_moved',$event$" + event + "$event$);")
        source_identity = identity(urls[0])
        before = sql(urls[0], "SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e")
        watermark = int(sql(urls[0], "SELECT max(id) FROM events"))
        backup = out / "pre-emission.dump"
        run(["pg_dump", "-Fc", "--file=" + str(backup), urls[0]])
        run(["pg_restore", "--exit-on-error", "--dbname=" + urls[1], str(backup)])
        after = sql(urls[1], "SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e")
        if before != after:
            raise RuntimeError("restored immutable events differ")
        restored_identity = identity(urls[1])
        if source_identity == restored_identity:
            raise RuntimeError("restore identity was reused")
        compatible = run([binary, "admin", "downgrade-check", "--database-url", urls[1]])
        pin = (root / "internal/queue/testdata/wire/v1/event_policy_authority_applied.json").read_text()
        sql(urls[0], "INSERT INTO events(queue_id,type,payload) VALUES(1,'policy_authority_applied',$event$" + pin + "$event$);")
        run([binary, "admin", "downgrade-check", "--database-url", urls[0]], expected=1)
        suffix = sql(urls[0], "SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM events e WHERE id>" + str(watermark))
        evidence = out / "recovered-post-backup-evidence.json"
        evidence.write_text(suffix + "\n")
        summary.update({"backup_sha256": hashlib.sha256(backup.read_bytes()).hexdigest(), "backup_watermark": watermark, "restored_watermark": int(sql(urls[1], "SELECT max(id) FROM events")), "source_database_identity": source_identity, "restored_database_identity": restored_identity, "immutable_events_identical": True, "post_backup_evidence_recovered": json.loads(suffix), "evidence_sha256": hashlib.sha256(evidence.read_bytes()).hexdigest(), "source_downgrade_exit": 1, "restored_downgrade_exit": 0, "restored_result": compatible.strip(), "verdict": "PASS"})
    finally:
        for name in reversed(created):
            sql(base, 'DROP DATABASE "' + name + '"')
        summary["scratch_databases_removed"] = created
        (out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps({"verdict": summary["verdict"], "scratch_databases_removed": len(created)}))


if __name__ == "__main__":
    main()
