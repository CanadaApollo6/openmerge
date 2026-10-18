#!/usr/bin/env python3
"""End-to-end synthetic export checks; no DB/network, run from any directory."""

import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

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
            append(30, "pr_observed", dict(pr=42, head_sha="synthetic-head", trusted=True, eligible=True))
            append(40, "enqueue_requested", dict(pr=42, at="2026-01-01T00:00:00Z"))
            result, data = run()
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(data["verdict"], "PASS")
            self.assertEqual(data["streams"]["candidate"]["checkpoints"], 4)
            result, data = run("--inject-divergence-event", "40")
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertEqual(data["first_difference"]["event_id"], 40)
            self.assertIn("injected-divergence", data["first_difference"]["candidate"])
            # The shadow reconciler must also reject same-digest missing bounds.
            journal[1]["payload"]["data"].pop("window")
            events.write_text("".join(json.dumps(e) + "\n" for e in journal))
            result, data = run()
            self.assertEqual(result.returncode, 1)
            self.assertEqual(data["gate4"], "FAIL")
            self.assertTrue(data["streams"]["candidate"]["policies"][0]["would_emit_config_applied"])

    def test_first_difference_and_truncation(self):
        left = '{"event_id":7,"queue_id":2}'
        right = '{"event_id":8,"queue_id":2}'
        self.assertEqual(replay.compare([left], [right])["event_id"], 7)
        self.assertEqual(replay.compare([], [right])["event_id"], 8)


if __name__ == "__main__":
    unittest.main()
