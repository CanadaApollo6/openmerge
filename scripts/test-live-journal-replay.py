#!/usr/bin/env python3
"""End-to-end synthetic export checks; no DB/network, run from any directory."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True

SCRIPT = Path(__file__).with_name("check-live-journal-replay.py")
spec = importlib.util.spec_from_file_location("replay", SCRIPT)
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)


class LiveReplayTest(unittest.TestCase):
    def test_identical_and_divergent(self):
        # Use HEAD's actual config via both loaders, but wholly invented queue,
        # PR, fence, timestamps and events. The first run obtains its digest;
        # the second establishes ConfigApplied history and must pass both gates.
        with tempfile.TemporaryDirectory(dir=Path.home() / ".cache") as temporary:
            root = Path(temporary)
            events, queues, report = (root / name for name in ("events.jsonl", "queues.jsonl", "report.json"))
            queues.write_text(json.dumps(dict(id=1, base="main", initial_fence="synthetic-fence")) + "\n")
            head = replay.git(replay.ROOT, "rev-parse", "HEAD")
            journal = []

            def append(event_id, kind, data):
                journal.append(dict(id=event_id, queue_id=1, type=kind,
                                    payload=dict(version=1, type=kind, data=data),
                                    dedupe_key=None, recorded_at="2026-01-01T00:00:00Z"))
                events.write_text("".join(json.dumps(e) + "\n" for e in journal))

            command = ["python3", str(SCRIPT), "--baseline", str(replay.ROOT),
                       "--events", str(events), "--queues", str(queues), "--out", str(report)]

            def run(*extra):
                result = subprocess.run(command + list(extra), capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 2, result.stdout + result.stderr)
                return result, json.loads(report.read_text())

            append(10, "base_moved", dict(sha=head))
            result, data = run()
            self.assertEqual(result.returncode, 1)  # no recorded policy: fail closed
            self.assertEqual(data["gate2"], "PASS")
            self.assertEqual(data["gate4"], "FAIL")
            digest = data["streams"]["candidate"]["policies"][0]["recomputed_digest"]
            # Explicit window bounds match this repo's live speculative config.
            append(20, "config_applied", dict(commit=head, digest=digest,
                                              window=dict(max_depth=4, min_depth=1)))
            result, data = run()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(data["actions_identical"], "2/2")
            self.assertEqual(data["state_identical"], "2/2")
            self.assertEqual(data["derived_determinism"], "PASS")
            self.assertEqual(data["non_allowlisted_fields"], [])
            for summary in data["streams"].values():
                stream = Path(summary["path"])
                self.assertTrue(stream.is_file())
                self.assertEqual(replay.hashlib.sha256(stream.read_bytes()).hexdigest(), summary["sha256"])
            result, data = run("--strict-state")
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            # Simulate the old encoder through a Go source overlay. Candidate
            # side projections are compared separately from the legacy fields.
            snapshot = replay.ROOT / "internal/queue/snapshot.go"
            legacy = root / "snapshot-v2.go"
            legacy.write_text(snapshot.read_text().replace("return json.Marshal(v)", '''
 raw, err := json.Marshal(v); if err != nil { return nil, err }
 var fields map[string]json.RawMessage
 if err := json.Unmarshal(raw, &fields); err != nil { return nil, err }
 fields["Version"] = json.RawMessage(`2`)
 delete(fields, "ChangeRevisions"); delete(fields, "BatchRevisions")
 return json.Marshal(fields)
'''))
            overlay = root / "v2-overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(snapshot): str(legacy)}}))
            bump = ("--baseline-overlay", str(overlay))
            result, data = run(*bump)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(data["state_identical"], "2/2")
            result, data = run(*bump, "--strict-state")
            self.assertEqual(result.returncode, 1)
            self.assertEqual(data["first_state_divergence"]["event_id"], 10)
            self.assertEqual(data["actions_identical"], "2/2")
            append(30, "pr_observed", dict(pr=42, head_sha="synthetic-head", trusted=True, eligible=True))
            append(40, "enqueue_requested", dict(pr=42, at="2026-01-01T00:00:00Z"))
            result, data = run(*bump)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(data["legacy_state_identical"], "4/4")
            self.assertEqual(data["derived_determinism"], "PASS")
            self.assertEqual(data["derived_nonempty_checkpoints"]["ChangeRevisions"], 2)
            # A non-allowlisted candidate addition fails at its first checkpoint.
            extra = root / "snapshot-extra.go"
            extra.write_text(snapshot.read_text().replace("return json.Marshal(v)", '''
 raw, err := json.Marshal(v); if err != nil { return nil, err }
 var fields map[string]json.RawMessage
 if err := json.Unmarshal(raw, &fields); err != nil { return nil, err }
 fields["Approvals"] = json.RawMessage(`["invented"]`)
 return json.Marshal(fields)
'''))
            candidate_overlay = root / "candidate-overlay.json"
            candidate_overlay.write_text(json.dumps({"Replace": {str(snapshot): str(extra)}}))
            result, data = run(*bump, "--candidate-overlay", str(candidate_overlay))
            self.assertEqual(result.returncode, 1)
            self.assertEqual(data["first_state_divergence"]["event_id"], 10)
            self.assertEqual(data["non_allowlisted_fields"][0]["fields"]["Approvals"]["candidate"], ["invented"])
            for mode in ((), ("--strict-state",)):
                result, data = run(*bump, *mode, "--inject-divergence-event", "40")
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertEqual(data["first_action_divergence"]["event_id"], 40)
                self.assertIn("injected-divergence", data["first_action_divergence"]["candidate"])
            # The shadow reconciler must also reject same-digest missing bounds.
            journal[1]["payload"]["data"].pop("window")
            events.write_text("".join(json.dumps(e) + "\n" for e in journal))
            result, data = run()
            self.assertEqual(result.returncode, 1)
            self.assertEqual(data["gate4"], "FAIL")
            self.assertTrue(data["streams"]["candidate"]["policies"][0]["would_emit_config_applied"])

    def test_derived_safety_and_determinism(self):
        def record(fields):
            return json.dumps(dict(kind="checkpoint", event_id=8, queue_id=1,
                                   revision=2, actions=[], snapshot_base64=replay.base64.b64encode(
                                       json.dumps(fields).encode()).decode()))
        baseline = record({"Version": 2})
        fields = {"Version": 3, "ChangeRevisions": {"42": {"Revision": 1}}}
        candidate = record(fields)
        repeat = record({"Version": 3, "ChangeRevisions": {"42": {"Revision": 2}}})
        data = replay.compare_checkpoints([baseline], [candidate], False, [repeat])
        self.assertEqual(data["derived_determinism"], "FAIL")
        self.assertEqual(data["first_derived_divergence"]["event_id"], 8)
        for name in ("Accountability", "Classification", "Approvals"):
            fields["ChangeRevisions"]["42"][name] = "invented"
            data = replay.compare_checkpoints([baseline], [record(fields)], False)
            self.assertIn(f"ChangeRevisions.42.{name}", data["non_allowlisted_fields"][0]["fields"])

    def test_checkpoint_truncation(self):
        record = json.dumps(dict(kind="checkpoint", event_id=7, queue_id=2,
                                 revision=1, actions=[], state_sha256="hash",
                                 snapshot_base64=replay.base64.b64encode(b'{"Version":3}').decode()))
        for old, new in (([record], []), ([], [record])):
            result = replay.compare_checkpoints(old, new, strict=True)
            self.assertEqual(result["actions_identical"], "0/1")
            self.assertEqual(result["first_action_divergence"]["event_id"], 7)
            self.assertEqual(result["first_state_divergence"]["event_id"], 7)


if __name__ == "__main__":
    unittest.main()
