#!/usr/bin/env python3
"""Mandatory offline replay preflight; synthetic inputs only, no CLI recursion."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
from types import SimpleNamespace
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SCRIPT = Path(__file__).with_name("check-live-journal-replay.py")
spec = importlib.util.spec_from_file_location("replay", SCRIPT)
replay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(replay)


def node(value, *, kind=None):
    """Small typed reflection fixture, independent of the snapshot codec."""
    if isinstance(value, dict):
        return dict(type="fixture", kind=kind or "struct", zero=not value,
                    value={key: node(item) for key, item in value.items()})
    if isinstance(value, list):
        return dict(type="[]fixture", kind="slice", zero=False, nil=False,
                    value=[node(item) for item in value])
    name = {bool: "bool", int: "int", float: "float64", str: "string"}[type(value)]
    return dict(type=name, kind=name, zero=not value, value=value)


def projection(records):
    return dict(type="map[string][]queue.BatchRevision", kind="map", zero=not records,
                nil=not records, value={json.dumps(node(key), sort_keys=True): node(value)
                                       for key, value in records.items()})


def checkpoint(event_id=8, records=None, **extra):
    fields = {"batchRevisions": projection(records or {})}
    result = dict(kind="checkpoint", event_id=event_id, queue_id=1, revision=2,
                  actions=[], state_sha256="same-snapshot", structural=node({}),
                  deadlines=[], boundary_probes=[])
    result["structural"]["value"] = fields
    result.update(extra)
    return json.dumps(result)


class ComparisonTest(unittest.TestCase):
    def test_struct_fields_cannot_collide_with_node_markers(self):
        baseline = node({"kind": "x", "zero": False})
        candidate = node({"kind": "x", "zero": True})
        self.assertTrue(replay.structural_differences(baseline, candidate))
        candidate = node({"kind": "x", "zero": False, "ReviewApprovals": False})
        differences = replay.structural_differences(baseline, candidate)
        self.assertIn("ReviewApprovals", differences)
        self.assertTrue(differences["ReviewApprovals"]["new_field"])

    def test_json_types_are_strict_everywhere(self):
        for left, right in ((True, 1), (1, 1.0), (False, 0)):
            for a, b in ((left, right), ([left], [right]), ({"x": left}, {"x": right})):
                self.assertFalse(replay.exact(a, b))
            self.assertTrue(replay.legacy_differences({"x": left}, {"x": right}))
            self.assertTrue(replay.structural_differences(node(left), node(right)))
        old = checkpoint(actions=[{"value": True}])
        new = checkpoint(actions=[{"value": 1}])
        result = replay.compare_checkpoints([old], [new], False)
        self.assertIsNotNone(result["first_action_divergence"])
        old = checkpoint(boundary_probes=[{"at": "t", "actions": [True]}])
        new = checkpoint(boundary_probes=[{"at": "t", "actions": [1]}])
        self.assertIsNotNone(replay.compare_checkpoints([old], [new], False)["first_boundary_divergence"])

    def test_hold_field_exceptions_are_exact_zero_only(self):
        nil_map = lambda typ: dict(type=typ, kind="map", zero=True, nil=True, value={})
        allowed = {
            "classifications": nil_map("queue.classifications"),
            "authority": dict(type="*queue.authorityProjection", kind="ptr", zero=True, nil=True),
            "heldRequests": nil_map("map[int]queue.HeldRequest"),
            "dependencyWaits": nil_map("map[int]queue.DependencyWait"),
        }
        for name, zero in allowed.items():
            with self.subTest(name=name):
                self.assertEqual(replay.structural_differences(node({}), node({}) | dict(value={name: zero})), {})
                for bad in (zero | dict(zero=False), zero | dict(nil=False), zero | dict(type="other"), node(False)):
                    differences = replay.structural_differences(node({}), node({}) | dict(value={name: bad}))
                    self.assertIn(name, differences)
        unknown = replay.structural_differences(node({}), node({}) | dict(value={"brandNew": nil_map("x")}))
        self.assertIn("brandNew", unknown)

    def test_pending_conditions_exception_matches_any_observation_shard(self):
        key = json.dumps(node(42), sort_keys=True)

        def observations(entry):
            shard = dict(type="map[int]fixture", kind="map", zero=False, nil=False, value={key: entry})
            shards = dict(type="[]fixture", kind="array", zero=False, value=[node({}), shard])
            return node({}) | dict(value={"observations": node({}) | dict(value={"shards": shards})})

        old = observations(node({"Head": "abc"}))
        empty = observations(node({"Head": "abc"}) | dict(value={"Head": node("abc"), "PendingConditions": node("")}))
        self.assertEqual(replay.structural_differences(old, empty), {})
        held = observations(node({"Head": "abc"}) | dict(value={"Head": node("abc"), "PendingConditions": node("ci")}))
        differences = replay.structural_differences(old, held)
        self.assertEqual(len(differences), 1)
        self.assertTrue(next(iter(differences)).endswith(".PendingConditions"))
        self.assertEqual(replay.exception_name(next(iter(differences))), "observations.shards[*].{pr}.PendingConditions")
        # The wildcard covers only per-PR observation entries, not other paths.
        elsewhere = node({}) | dict(value={"PendingConditions": node("")})
        self.assertIn("PendingConditions", replay.structural_differences(node({}), elsewhere))

    def test_approval_exception_is_nil_only(self):
        old = checkpoint()
        value = json.loads(old)
        fields = value["structural"]["value"]
        fields["approvalFacts"] = dict(type="queue.approvalProjection", kind="map", zero=True, nil=True, value={})
        self.assertIsNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])
        for bad in (dict(type="queue.approvalProjection", kind="map", zero=False, nil=False, value={}), node(False), dict(type="queue.approvalProjection", kind="map", zero=False, nil=False, value={json.dumps(node("change"), sort_keys=True): node({})})):
            fields["approvalFacts"] = bad
            self.assertIsNotNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])

    def test_approval_wait_exception_is_nil_only(self):
        old = checkpoint()
        value = json.loads(old)
        fields = value["structural"]["value"]
        fields["approvalWaits"] = dict(type="map[queue.ChangeID]queue.ApprovalWait", kind="map", zero=True, nil=True, value={})
        self.assertIsNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])
        for bad in (dict(type="map[queue.ChangeID]queue.ApprovalWait", kind="map", zero=False, nil=False, value={}), node(False), dict(type="map[queue.ChangeID]queue.ApprovalWait", kind="map", zero=False, nil=False, value={json.dumps(node("change"), sort_keys=True): node({})})):
            fields["approvalWaits"] = bad
            self.assertIsNotNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])

    def test_receipt_exception_is_nil_only(self):
        old = checkpoint()
        value = json.loads(old)
        fields = value["structural"]["value"]
        fields["receipts"] = dict(type="queue.receiptProjection", kind="map", zero=True, nil=True, value={})
        self.assertIsNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])
        for bad in (dict(type="queue.receiptProjection", kind="map", zero=False, nil=False, value={}), node(False), dict(type="queue.receiptProjection", kind="map", zero=False, nil=False, value={json.dumps(node("change"), sort_keys=True): node({})})):
            fields["receipts"] = bad
            self.assertIsNotNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])

    def test_landing_decision_exception_is_nil_only(self):
        old = checkpoint()
        value = json.loads(old)
        fields = value["structural"]["value"]
        typename = "map[string]queue.LandingDecisionRecorded"
        fields["landingDecisions"] = dict(type=typename, kind="map", zero=True, nil=True, value={})
        self.assertIsNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])
        for bad in (dict(type=typename, kind="map", zero=False, nil=False, value={}), node(False), dict(type=typename, kind="map", zero=False, nil=False, value={json.dumps(node("decision"), sort_keys=True): node({})})):
            fields["landingDecisions"] = bad
            self.assertIsNotNone(replay.compare_checkpoints([old], [json.dumps(value)], False)["first_state_divergence"])

    def test_unreviewed_zero_field_fails(self):
        old = checkpoint()
        value = json.loads(old)
        value["structural"]["value"]["ReviewApprovals"] = node(False)
        result = replay.compare_checkpoints([old], [json.dumps(value)], False)
        self.assertIn("ReviewApprovals", result["first_state_divergence"]["differing_fields"])
        self.assertTrue(result["non_allowlisted_fields"])

    def test_derived_completeness_and_forbidden_data(self):
        change = dict(ChangeID="pr:42", Revision=1, HeadSHA="head",
                      Basis=dict(base_sha="base", at="2026-01-01T00:00:00Z"), LogPosition=3)
        batch = dict(PR=42, Revision=1, HeadSHA="head")
        fields = dict(ChangeRevisions={"42": change}, BatchRevisions={"b1": [batch]})
        self.assertEqual(replay.derived_violations(fields), {})
        for container, record in (("ChangeRevisions", change), ("BatchRevisions", batch)):
            for field in record:
                malformed = copy.deepcopy(fields)
                item = malformed[container]["42"] if container == "ChangeRevisions" else malformed[container]["b1"][0]
                del item[field]
                self.assertTrue(replay.derived_violations(malformed), (container, field))
            for field, bad in (("Revision", 0), ("Revision", True), ("Revision", 1.0), ("HeadSHA", "")):
                malformed = copy.deepcopy(fields)
                item = malformed[container]["42"] if container == "ChangeRevisions" else malformed[container]["b1"][0]
                item[field] = bad
                self.assertTrue(replay.derived_violations(malformed), (container, field, bad))
            for field in ("Accountability", "Classification", "Approvals"):
                malformed = copy.deepcopy(fields)
                item = malformed[container]["42"] if container == "ChangeRevisions" else malformed[container]["b1"][0]
                item[field] = False
                self.assertTrue(replay.derived_violations(malformed), (container, field))

    def test_empty_malformed_projection_containers_fail(self):
        for field in replay.DERIVED_FIELDS:
            for malformed in (False, 0, []):
                self.assertIn(field, replay.derived_violations({field: malformed}),
                              (field, malformed))

    def test_derived_wrapper_unknown_zero_field_fails(self):
        old = checkpoint()
        value = json.loads(old)
        value["structural"]["value"]["changeRevisions"] = dict(
            type="queue.changeRevisions", kind="struct", zero=False,
            value={"shards": dict(type="*[32]map[int]queue.RevisionRecord", kind="ptr",
                                  zero=True, nil=True),
                   "ReviewApprovals": node(False)})
        result = replay.compare_checkpoints([old], [json.dumps(value)], False)
        self.assertTrue(result["non_allowlisted_fields"])
        self.assertIn("candidate.changeRevisions", result["non_allowlisted_fields"][0]["fields"])

    def test_derived_conditional_equality_and_repeat(self):
        first = {"b1": [dict(PR=42, Revision=1, HeadSHA="head")]}
        second = {"b1": [dict(PR=42, Revision=2, HeadSHA="head")]}
        empty, populated, changed = checkpoint(), checkpoint(records=first), checkpoint(records=second)
        result = replay.compare_checkpoints([empty], [populated], False, [populated])
        self.assertIsNone(result["first_state_divergence"])
        self.assertEqual(result["non_allowlisted_fields"], [])
        result = replay.compare_checkpoints([populated], [changed], False, [changed])
        self.assertIsNotNone(result["first_state_divergence"])
        self.assertTrue(result["non_allowlisted_fields"])
        result = replay.compare_checkpoints([empty], [populated], False, [changed])
        self.assertEqual(result["derived_determinism"], "FAIL")
        self.assertEqual(result["first_derived_divergence"]["event_id"], 8)

    def test_boundary_cap_report_uses_both_probe_counts(self):
        for field in ("deadlines", "boundary_probes"):
            capped = checkpoint(**{field: [None] * 128})
            for old, new in ((capped, checkpoint()), (checkpoint(), capped)):
                result = replay.compare_checkpoints([old], [new], False)
                self.assertTrue(result["boundary_cap_hit"])
        self.assertFalse(replay.compare_checkpoints([checkpoint()], [checkpoint()], False)["boundary_cap_hit"])

    def test_checkpoint_truncation_and_identity(self):
        record = checkpoint(event_id=7)
        for old, new in (([record], []), ([], [record]), ([record], [checkpoint(event_id=True)])):
            result = replay.compare_checkpoints(old, new, strict=True)
            self.assertEqual(result["actions_identical"], "0/1")
            self.assertIsNotNone(result["first_action_divergence"])
            self.assertIsNotNone(result["first_state_divergence"])
            self.assertIsNotNone(result["first_boundary_divergence"])


class PolicySourceTest(unittest.TestCase):
    """A journal from another repository reads its policy from that repository's clone."""

    OLD = "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"
    NEW = OLD + "  check_timeout: 10m\n"

    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="policy-source-")
        self.directory = Path(self.temporary.name)
        self.clone = self.directory / "clone"
        self.clone.mkdir()
        self.git("init", "--quiet", "--initial-branch", "trunk")
        self.old = self.commit(self.OLD)
        self.new = self.commit(self.NEW)
        self.write_export("Acme/API")

    def tearDown(self):
        self.temporary.cleanup()

    def git(self, *args):
        return replay.git(self.clone, "-c", "user.name=replay", "-c", "user.email=replay@example.invalid",
                          "-c", "commit.gpgsign=false", *args)

    def commit(self, config):
        (self.clone / ".openmerge.yml").write_text(config)
        self.git("add", ".openmerge.yml")
        self.git("commit", "--quiet", "--message", "policy")
        return self.git("rev-parse", "HEAD")

    def write_export(self, repository):
        queue = dict(id=7, installation="1", repository=repository, base="trunk", initial_fence="f0")
        (self.directory / "queues.jsonl").write_text(json.dumps(queue) + "\n")
        events = [("base_moved", dict(sha=self.old)), ("config_applied", dict(commit=self.new, digest="d"))]
        (self.directory / "events.jsonl").write_text("".join(
            json.dumps(dict(id=index, queue_id=7, type=kind, payload=dict(version=1, type=kind, data=data),
                            recorded_at="2026-01-01T00:00:00Z")) + "\n"
            for index, (kind, data) in enumerate(events, 1)))

    def prepare(self, *policy_repo):
        return replay.prepare(SimpleNamespace(
            events=self.directory / "events.jsonl", queues=self.directory / "queues.jsonl", config_file=None,
            policy_repo=list(policy_repo), default_branch="main", poll_interval_seconds=60, keep_head_branch=False))

    def test_queue_reads_policy_from_its_clone(self):
        # The journal spells the repository "Acme/API"; GitHub names ignore case.
        inputs = self.prepare(f"acme/api={self.clone}")
        self.assertEqual(inputs["Policies"], {self.old: self.OLD, self.new: self.NEW})
        self.assertEqual([event["Policy"] for event in inputs["Events"]], [self.old, self.new])
        queue = inputs["Queues"][0]
        self.assertEqual(queue["policy_live"], self.NEW)
        self.assertEqual(queue["policy_default_branch"], "trunk")
        source, = inputs["PolicySources"]
        self.assertEqual((source["commit"], source["default_branch"], source["queue_ids"]), (self.new, "trunk", [7]))
        self.assertEqual(source["config_sha256"], replay.hashlib.sha256(self.NEW.encode()).hexdigest())
        # The OpenMerge checkout still supplies the fallback live policy.
        self.assertEqual(inputs["Live"], replay.config_at(replay.ROOT, "HEAD"))

    def test_unmapped_foreign_commit_names_the_option(self):
        with self.assertRaisesRegex(ValueError, "--policy-repo"):
            self.prepare()

    def test_mapping_without_a_queue_is_refused(self):
        with self.assertRaisesRegex(ValueError, "no queue in the export"):
            self.prepare(f"acme/api={self.clone}", f"acme/other={self.clone}")

    def test_malformed_and_duplicate_mappings_are_refused(self):
        for values in (["acme/api"], ["acme=x"], ["/api=x"], [f"acme/api={self.clone}", f"Acme/API={self.clone}"]):
            with self.assertRaisesRegex(ValueError, "--policy-repo"):
                self.prepare(*values)

    def test_detached_clone_is_refused(self):
        self.git("checkout", "--quiet", "--detach")
        with self.assertRaisesRegex(ValueError, "default branch checked out"):
            self.prepare(f"acme/api={self.clone}")


class SyntheticProbeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cache = Path.home() / ".cache/openmerge-replay"
        cache.mkdir(parents=True, exist_ok=True)
        cls.temporary = tempfile.TemporaryDirectory(prefix="synthetic-preflight-", dir=cache)
        cls.directory = Path(cls.temporary.name)
        cls.args = SimpleNamespace(stream_directory=cls.directory, baseline_overlay=None,
                                   candidate_overlay=None, inject_divergence_event=None)
        config = "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n  check_timeout: 10m\n"
        cls.inputs = dict(Queues=[dict(id=1, base="main", initial_fence="f0")],
                          Policies={"synthetic": config}, Live=config, DefaultBranch="main",
                          PollSeconds=60, DeleteHead=True, Events=[])
        events = [
            ("base_moved", dict(sha="base")),
            ("pr_observed", dict(pr=42, head_sha="head", trusted=True, eligible=True)),
            ("enqueue_requested", dict(pr=42, at="2026-01-01T00:00:00Z")),
            ("batch_started", dict(batch_id="b1", base_sha="base", prs=[dict(pr=42, head_sha="head")], at="2026-01-01T00:00:00Z")),
            ("test_ref_built", dict(batch_id="b1", tested_sha="tested", method="merge", at="2026-01-01T00:00:00Z")),
            ("checks_refreshed", dict(batch_id="b1", tested_sha="tested", at="2026-01-01T00:10:00Z")),
        ]
        for index, (kind, data) in enumerate(events, 1):
            cls.inputs["Events"].append(dict(id=index, queue_id=1, type=kind,
                payload=dict(version=1, type=kind, data=data), Policy="synthetic",
                recorded_at="2026-01-01T00:10:02Z" if index == 6 else "2026-01-01T00:00:00Z"))
        (cls.directory / "probe_test.go").write_text(replay.PROBE)
        cls.write_input()
        cls.baseline = replay.probe(replay.ROOT, cls.directory, "baseline", cls.args)

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    @classmethod
    def write_input(cls):
        (cls.directory / "input.json").write_text(json.dumps(cls.inputs))

    def overlay(self, changes):
        replacements = {}
        for filename, before, after in changes:
            source = replay.ROOT / filename
            content = source.read_text()
            self.assertIn(before, content)
            replacement = self.directory / (source.stem + "-modified.go")
            replacement.write_text(content.replace(before, after, 1))
            replacements[str(source)] = str(replacement)
        path = self.directory / "candidate-overlay.json"
        path.write_text(json.dumps({"Replace": replacements}))
        self.args.candidate_overlay = path

    def tearDown(self):
        self.args.candidate_overlay = None
        self.args.baseline_overlay = None
        self.inputs.pop("Boundaries", None)
        self.write_input()

    @unittest.skipUnless(os.environ.get("OM_REPLAY_PREFLIGHT_REQUEST"),
                         "no deployment pair requested")
    def test_deployment_pair(self):
        request = json.loads(os.environ["OM_REPLAY_PREFLIGHT_REQUEST"])
        inputs = copy.deepcopy(self.inputs)
        inputs.pop("Boundaries", None)
        with tempfile.TemporaryDirectory(prefix="deployment-pair-", dir=self.directory) as temporary:
            directory = Path(temporary)
            args = SimpleNamespace(
                stream_directory=directory,
                baseline_overlay=Path(request["baseline_overlay"]) if request.get("baseline_overlay") else None,
                candidate_overlay=Path(request["candidate_overlay"]) if request.get("candidate_overlay") else None,
                inject_divergence_event=None)
            baseline, candidate = [Path(request[key]) for key in ("baseline", "candidate")]
            (directory / "probe_test.go").write_text(replay.PROBE)
            input_path = directory / "input.json"
            input_path.write_text(json.dumps(inputs))
            old = replay.probe(baseline, directory, "baseline", args)
            new = replay.probe(candidate, directory, "candidate", args)
            inputs["Boundaries"] = replay.boundary_union(old, new)
            input_path.write_text(json.dumps(inputs))
            old = replay.probe(baseline, directory, "baseline", args)
            new = replay.probe(candidate, directory, "candidate", args)
            repeated = replay.probe(candidate, directory, "candidate-repeat", args)
            result = replay.compare_checkpoints(old, new, False, repeated)
        # Persist the diagnostic before assertions, so a refused deployment still
        # records the synthetic divergence beside its real-export evidence.
        Path(os.environ["OM_REPLAY_PREFLIGHT_RESULT"]).write_text(
            json.dumps(result, indent=2, sort_keys=True) + "\n")
        for key in ("first_action_divergence", "first_state_divergence",
                    "first_boundary_divergence", "first_derived_divergence"):
            self.assertIsNone(result[key], f"deployment synthetic {key}: {result[key]}")
        self.assertEqual(result["non_allowlisted_fields"], [],
                         f"deployment synthetic unreviewed fields: {result['non_allowlisted_fields']}")

    def test_identical_full_fold_passes(self):
        result = replay.compare_checkpoints(self.baseline, self.baseline, False, self.baseline)
        self.assertEqual(result["actions_identical"], "7/7")
        self.assertEqual(result["structural_state_identical"], "7/7")
        self.assertEqual(result["derived_determinism"], "PASS")
        self.assertEqual(result["non_allowlisted_fields"], [])
        self.assertIsNone(result["first_boundary_divergence"])

    def test_queue_policy_replaces_shared_policy(self):
        # The shared values alone cannot resolve this queue: no queue in the
        # shared live policy targets "main" when the default branch is "other".
        queue = self.inputs["Queues"][0]
        shared = self.inputs["Live"], self.inputs["DefaultBranch"]
        queue.update(policy_live=self.inputs["Live"], policy_default_branch="main")
        self.inputs.update(Live="version: 1\nqueues: []\n", DefaultBranch="other")
        try:
            self.write_input()
            lines = replay.probe(replay.ROOT, self.directory, "candidate-queue-policy", self.args)
        finally:
            self.inputs["Live"], self.inputs["DefaultBranch"] = shared
            del queue["policy_live"], queue["policy_default_branch"]
            self.write_input()
        self.assertEqual(lines, self.baseline)

    def test_final_checkpoint_includes_queue_without_events(self):
        self.inputs["Queues"].append(dict(id=2, base="main", initial_fence="f2"))
        try:
            self.write_input()
            lines = replay.probe(replay.ROOT, self.directory, "candidate-final", self.args)
        finally:
            self.inputs["Queues"].pop()
            self.write_input()
        final = [json.loads(line) for line in lines
                 if json.loads(line).get("kind") == "checkpoint" and json.loads(line).get("final")]
        self.assertEqual([record["queue_id"] for record in final], [1, 2])
        self.assertEqual([record["boundary_key"] for record in final], ["final:1", "final:2"])
        self.assertEqual(final[1]["event_id"], 0)
        union = replay.boundary_union(lines, lines)
        self.assertIn("final:1", union)
        self.assertIn("final:2", union)
        self.assertIn("6", union)

    def test_state_addition_outside_snapshot_codec_fails(self):
        self.overlay([
            ("internal/queue/types.go", "type State struct {", "type State struct {\n ReviewApprovals bool"),
            ("internal/queue/apply.go", "func Apply(s State, e Event) State {", "func Apply(s State, e Event) State {\n s.ReviewApprovals = true"),
        ])
        candidate = replay.probe(replay.ROOT, self.directory, "candidate-field", self.args)
        result = replay.compare_checkpoints(self.baseline, candidate, False)
        self.assertEqual(result["snapshot_hashes_identical"], "7/7")
        self.assertEqual(result["actions_identical"], "7/7")
        self.assertEqual(result["first_state_divergence"]["event_id"], 1)
        self.assertIn("ReviewApprovals", result["first_state_divergence"]["differing_fields"])

    def test_distinct_pointer_keys_with_equal_pointees_fail_closed(self):
        # Astra's repro: baseline has two distinct pointers to 7; candidate one.
        changes = [
            ("internal/queue/types.go", "type State struct {", "type State struct {\n ReviewIndex map[*int]bool"),
            ("internal/queue/apply.go", "func Apply(s State, e Event) State {",
             "func Apply(s State, e Event) State {\n a,b:=7,7; s.ReviewIndex=map[*int]bool{&a:true,&b:true}"),
        ]
        self.overlay(changes)
        self.args.baseline_overlay = self.args.candidate_overlay
        self.args.candidate_overlay = None
        with self.assertRaisesRegex(RuntimeError, "canonical map-key collision at State.ReviewIndex"):
            replay.probe(replay.ROOT, self.directory, "baseline", self.args)
        self.args.baseline_overlay = None
        changes[1] = (changes[1][0], changes[1][1],
                      "func Apply(s State, e Event) State {\n a:=7; s.ReviewIndex=map[*int]bool{&a:true}")
        self.overlay(changes)
        candidate = replay.probe(replay.ROOT, self.directory, "candidate-one-key", self.args)
        index = json.loads(candidate[0])["structural"]["value"]["ReviewIndex"]
        self.assertEqual(len(index["value"]), 1)
        key = json.loads(next(iter(index["value"])))
        self.assertEqual(key["value"]["value"], 7)

    def test_cycles_fail_closed_with_field_path(self):
        for field_type, setup in (
            ("*any", "var x any; x=&x; s.ReviewCycle=&x"),
            ("map[string]any", 's.ReviewCycle=map[string]any{}; s.ReviewCycle["self"]=s.ReviewCycle'),
            ("[]any", "s.ReviewCycle=make([]any,1); s.ReviewCycle[0]=s.ReviewCycle"),
        ):
            with self.subTest(field_type=field_type):
                self.overlay([
                    ("internal/queue/types.go", "type State struct {",
                     "type State struct {\n ReviewCycle " + field_type),
                    ("internal/queue/apply.go", "func Apply(s State, e Event) State {",
                     "func Apply(s State, e Event) State {\n " + setup),
                ])
                with self.assertRaisesRegex(RuntimeError, r"structural cycle at State.ReviewCycle.*ancestor State.ReviewCycle"):
                    replay.probe(replay.ROOT, self.directory, "candidate-cycle", self.args)

    def test_shared_acyclic_pointers_pass(self):
        self.overlay([
            ("internal/queue/types.go", "type State struct {", "type State struct {\n ReviewShared [2]*int"),
            ("internal/queue/apply.go", "func Apply(s State, e Event) State {",
             "func Apply(s State, e Event) State {\n x:=7; s.ReviewShared=[2]*int{&x,&x}"),
        ])
        candidate = replay.probe(replay.ROOT, self.directory, "candidate-shared", self.args)
        shared = json.loads(candidate[0])["structural"]["value"]["ReviewShared"]["value"]
        self.assertEqual(shared[0], shared[1])

    def test_timeout_shift_fails_at_union_boundary(self):
        self.overlay([("internal/queue/plan.go", "timedOut := timeoutEnabled && now.After(deadline)",
                      "timedOut := timeoutEnabled && now.After(deadline.Add(time.Second))")])
        candidate = replay.probe(replay.ROOT, self.directory, "candidate-timer", self.args)
        self.inputs["Boundaries"] = replay.boundary_union(self.baseline, candidate)
        # The exported checkpoint is already after the deadline and the
        # injected +1s delay: event-time plans agree, elapsed probes must not.
        self.assertTrue(self.inputs["Boundaries"]["6"])
        self.write_input()
        old = replay.probe(replay.ROOT, self.directory, "baseline", self.args)
        new = replay.probe(replay.ROOT, self.directory, "candidate-timer", self.args)
        result = replay.compare_checkpoints(old, new, False)
        self.assertEqual(result["actions_identical"], "7/7")
        self.assertEqual(result["structural_state_identical"], "7/7")
        self.assertEqual(result["first_boundary_divergence"]["event_id"], 6)
        self.assertGreater(result["boundary_probe_count"], 0)
        final_old, final_new = [json.loads(lines[5]) for lines in (old, new)]
        instant = "2026-01-01T00:10:00.000000001Z"
        plans = [next(p["actions"] for p in record["boundary_probes"] if p["at"] == instant)
                 for record in (final_old, final_new)]
        self.assertEqual([a["type"] for a in plans[0]], ["dequeue", "delete_ref"])
        self.assertEqual(plans[1], [])

    def test_boundary_cap_fails_closed(self):
        self.inputs["Boundaries"] = {"6": [f"2026-01-01T01:{i:02d}:00Z" for i in range(43)]}
        self.write_input()
        with self.assertRaisesRegex(RuntimeError, "cap hit"):
            replay.probe(replay.ROOT, self.directory, "candidate-cap", self.args)


if __name__ == "__main__":
    # The startup preflight runs this same suite directly, never the CLI main().
    if "--preflight" in sys.argv:
        sys.argv.remove("--preflight")
    unittest.main()
