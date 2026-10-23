#!/usr/bin/env python3
"""Offline 10a-2b-1 proof; uses shared test Postgres schemas, never a scratch database."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time

sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url

ROOT = Path(__file__).resolve().parents[1]
BASE = "4b1fe46"
SLICE = "10a-2b-1"
CLASSIFICATION = "Inert by own behaviour; depends on Hold; deploy after B"
EXTRA_PACKAGES = []
EXTRA_MUTATIONS = []


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def mutation_proofs(measured, out, pattern):
    """Compile overlay mutants, then require focused semantic tests to kill them."""
    targets = [
        ("reuse-spool-decode-storage", "spool.go", "c.value = spoolRecord{}", "// reuse cursor storage"),
        ("missing-required-source", "evidence.go", "required && len(found) != 1", "false"),
        ("oldest-observation", "evidence.go", "DESC,e.id DESC LIMIT 1", "ASC,e.id ASC LIMIT 1"),
        ("accept-wrong-check-result", "evidence.go", "string(check.Result) != newest.State", "false"),
        ("accept-invalid-role", "export.go", 'return role == "scope" || role == "dependency"', "return true"),
        ("omit-exact-closure", "export.go", "return verifyExactClosure(ctx, tx, spoolDir, rootsPath, selectedPath)", "return nil"),
    ]
    targets += [
        ("absence-source-id", "evidence.go", 'r.Source != nil || r.Relation != "relied_on"', 'r.Relation != "relied_on"'),
        ("adjacent-reference-duplicates", "evidence.go", 'bytes.Compare(previous, raw) >= 0', 'bytes.Compare(previous, raw) > 0'),
        ("duplicate-identity", "evidence.go", 'len(found) > 1 || required', 'required'),
        ("owning-decision", "evidence.go", 'if !matched {', 'if false && matched {'),
        ("conflicting-spool-payload", "spool.go", '!bytes.Equal(a.Payload, b.Payload)', 'false'),
        ("conflicting-spool-references", "spool.go", '!bytes.Equal(x, y)', 'false && !bytes.Equal(x, y)'),
        ("event-tie-break", "evidence.go", 'DESC,e.id DESC LIMIT 1', 'DESC,e.id ASC LIMIT 1'),
        ("newest-run-in-event", "evidence.go", 'run.ID > newest.ID ||', 'run.ID < newest.ID ||'),
        ("authority-always-required", "evidence.go", 'v.(queue.PolicyAuthorityApplied).Authority == "openmerge"', 'true || v.(queue.PolicyAuthorityApplied).Authority == "openmerge"'),
        ("authority-never-required", "evidence.go", 'v.(queue.PolicyAuthorityApplied).Authority == "openmerge"', 'false && v.(queue.PolicyAuthorityApplied).Authority == "openmerge"'),
        ("queue-id-order", "export.go", 'return a.QueueID < b.QueueID', 'return false'),
        ("exact-closure", "spool.go", 'func verifyExactClosure(ctx context.Context, tx pgx.Tx, dir, roots, selected string) error {', 'func verifyExactClosure(ctx context.Context, tx pgx.Tx, dir, roots, selected string) error { return nil;'),
        ("valid-resolution", "spool.go", 'if !validReferences(refs) {', 'if true { return true }; if !validReferences(refs) {'),
        # Replay's per-record re-resolution was subsumed by exact closure, which
        # now owns resolved-reference, absence and retained-source agreement.
        ("resolved-references", "spool.go", 'if e != nil || !bytes.Equal(wantRaw, gotRaw) {', 'if e != nil || false && !bytes.Equal(wantRaw, gotRaw) {'),
        ("missing-retained-source", "spool.go", 'if !expected.ok || !actual.ok {', 'if !actual.ok { break }; if !expected.ok {'),
        # closure's validResolution call is a defensive invariant: lookup always
        # appends a reference and its source together, so removing the call is
        # an equivalent mutant. Its logic is pinned directly by
        # TestValidResolutionRequiresExactReferenceAndDependencySets.
        ("scope-range", "export.go", 'if (s.Role == "scope") != inRange {', 'if false && inRange {'),
        ("owning-decision-id", "evidence.go", 'if item.DecisionEventID == event.source.ID {', 'if true {'),
        ("conflicting-spool-watermark", "spool.go", 'if b.MaxID != 0 && a.MaxID != b.MaxID {', 'if false {'),
        ("identity-structure", "evidence.go", '!validIdentity(r.Identity)', 'r.Identity == ""'),
        ("identity-canonical", "evidence.go", 'return evidenceIdentity(parts...) == identity', 'return true'),
        ("identity-html-escape", "evidence.go", 'e.SetEscapeHTML(false)', 'e.SetEscapeHTML(true)'),
        ("normalize-encode-error", "evidence.go", 'if err != nil { return nil, errCanonical } items[i] = keyed{r, raw}', '_ = err; items[i] = keyed{r, raw}'),
        ("multi-run-oldest-run-sql", "evidence.go", "ORDER BY (run->>'id')::numeric DESC,greatest", "ORDER BY (run->>'id')::numeric ASC,greatest"),
        ("multi-run-lowest-attempt-sql", "evidence.go", "greatest((run->>'attempt')::numeric,1) DESC LIMIT 1) DESC", "greatest((run->>'attempt')::numeric,1) ASC LIMIT 1) DESC"),
        ("authority-governing-commit", "evidence.go", "value.(queue.PolicyAuthorityApplied).GoverningCommit != ci.GoverningCommit", "value.(queue.PolicyAuthorityApplied).GoverningCommit != ci.GoverningCommit && false"),
        ("dispatch-generation-sql", "evidence.go", "match = ` AND payload->'data'->>'generation'=$6`", "match = ` AND $6::text IS NOT NULL`"),
        ("additional-transaction", "source.go", "if err = fn(ctx, tx); err != nil {", ""),
    ]
    targets += [
        ("pin-skip-config-fold", "evidence.go", "f.state = queue.Apply(f.state, v)", "// ignore config transition"),
        # Do not count helper-only removals of pin Validate/Apply as behaviour
        # kills: production decode already validates, and its pin value is not
        # read back here. Mutate actual rejection at the retained-source boundary.
        ("pin-ignore-rejected-decode", "evidence.go", 'v, err := decodeEvidence(&e)\n\t\tif err != nil {\n\t\t\tif e.source.Type == "policy_authority_applied" {\n\t\t\t\treturn 0, ErrPinRejected\n\t\t\t}\n\t\t\treturn 0, errSource\n\t\t}', 'v, err := decodeEvidence(&e)\n\t\tif err != nil { continue }'),
        ("pin-ignore-digest", "evidence.go", "v.Validate() != nil || v.EffectiveDigest != f.state.ConfigDigest", "v.Validate() != nil"),
        ("pin-hide-rejection", "evidence.go", "if fold.rejected {", "if false {"),
        ("rollback-status-recorded", "evidence.go", 'r.refs[len(r.refs)-1].Status = "inapplicable"', 'r.refs[len(r.refs)-1].Status = "recorded"'),
        ("rollback-status-absent", "evidence.go", 'r.refs[len(r.refs)-1].Status = "inapplicable"', 'r.refs[len(r.refs)-1].Status = "not_recorded"'),
        ("inapplicable-shape", "evidence.go", 'r.Status == "inapplicable" && (', 'false && ('),
        # This changes SQL selection across events, not newestCheckRun's Go
        # normalization of the chosen event's diagnostic/reference fields.
        ("sql-zero-attempt-not-normalized", "evidence.go", "ARRAY[(run->>'id')::numeric,greatest((run->>'attempt')::numeric,1)]", "ARRAY[(run->>'id')::numeric,(run->>'attempt')::numeric]"),
        # Deliberately report this defensive call's equivalence separately.
        ("closure-valid-resolution-call", "spool.go", "if !validResolution(r.Source, refs, deps) {", "if false {"),
    ]
    targets += EXTRA_MUTATIONS
    equivalent = {"closure-valid-resolution-call"}
    runs = "FROM jsonb_array_elements(e.payload->'data'->'runs') run WHERE run->>'name'=$6 AND run->>'source'=$7"
    tuple_pick = "(SELECT ARRAY[(run->>'id')::numeric,greatest((run->>'attempt')::numeric,1)] " + runs + " ORDER BY (run->>'id')::numeric DESC,greatest((run->>'attempt')::numeric,1) DESC LIMIT 1)"
    maxima = "(SELECT ARRAY[max((run->>'id')::numeric),max(greatest((run->>'attempt')::numeric,1))] " + runs + ")"
    targets.append(("independent-run-maxima", "evidence.go", tuple_pick, maxima))
    targets.append(("retain-inapplicable-check", "evidence.go", " AND $5::boolean", " AND ($5::boolean OR true)"))
    # Each family must retain its exact source edge, not merely a kind label.
    for typ in ("change_classified", "change_revision_observed", "review_stance_observed",
                "approval_receipt_recorded", "landing_checks_recorded", "landing_abandoned",
                "landing_decision_recorded", "approval_eligibility_observed", "batch_started",
                "test_ref_built", "change_accountability_observed", "policy_authority_applied",
                "landing_started", "landed", "checks_observed", "check_rerun_requested",
                "check_rerun_dispatch_progress", "check_rerun_dispatched"):
        old = 'ref := Reference{Kind: kind, Relation: "relied_on"'
        new = ('if typ == "' + typ + '" { if len(found)==1 { return &found[0], nil }; return nil,nil }\n\t' + old)
        targets.append(("drop-family-" + typ, "evidence.go", old, new))
    # Pin every still-required call site independently. Authority itself is now
    # the historical mode discriminator, rather than an unconditionally required
    # lookup; its old always-required mutant is covered above.
    evidence = (ROOT / "internal/audit/evidence.go").read_text()
    for i, match in enumerate(re.finditer(r"r\.lookup\([^\n]+, true,", evidence)):
        call = match.group()
        targets.append(("required-call-" + str(i), "evidence.go", call, call.rsplit(", true,", 1)[0] + ", false,"))
    targets.append(("required-checks", "evidence.go", ', r.authority, batch, attempt, r.authority)', ', false, batch, attempt, r.authority)'))
    targets += [
        ("store-cardinality", "internal/store/landing_evidence.go", "len(reads) != 1", "len(reads) < 1"),
        ("store-empty-batch", "internal/store/landing_evidence.go", 'if batch == "" || attempt == "" {', 'if false {'),
        ("store-decision-cap", "internal/store/landing_evidence.go", "octet_length(payload::text)<=4194304", "true"),
        ("store-eligibility-cap", "internal/store/landing_evidence.go", "CASE WHEN octet_length(payload::text)<=4194304 THEN payload ELSE NULL END FROM events WHERE queue_id=$1 AND type='approval_eligibility_observed'", "payload FROM events WHERE queue_id=$1 AND type='approval_eligibility_observed'"),
    ]
    results = []
    for name, file, old, new in targets:
        source = ROOT / (file if "/" in file else "internal/audit/" + file)
        text = source.read_text()
        if name == "additional-transaction":
            old = "if err = fn(ctx, tx); err != nil {"
            new = "extra, extraErr := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}); if extraErr != nil { return errSource }; _ = extra.Rollback(ctx); " + old
        # Whitespace changes are irrelevant to the mutation intent.
        match = re.search(r"\s*".join(re.escape(x) for x in old.split()), text)
        if match is None:
            raise ValueError("mutation target changed: " + name)
        text = text[:match.start()] + new + text[match.end():]
        if name == "accept-invalid-role":
            # Exact closure compares roles too; remove it for this one
            # conceptual mutant so the enum guard itself is exercised.
            text = text.replace("return verifyExactClosure(ctx, tx, spoolDir, rootsPath, selectedPath)", "return nil")
        if name == "exact-closure":
            text = text.replace('if !expected.ok || !actual.ok {', 'if false {')
        if name == "accept-invalid-role":
            # Remove both independent role guards for this one conceptual
            # mutant; leaving the range/role guard intact masks enum removal.
            guard = 'if (s.Role == "scope") != inRange {'
            if guard not in text:
                raise ValueError("role/range mutation target changed")
            text = text.replace(guard, "if false && inRange {", 1)
        work = out / "mutations" / name
        work.mkdir(parents=True, exist_ok=True)
        replacement = work / Path(file).name
        replacement.write_text(text)
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(source): str(replacement)}}))
        packages = ["./internal/store"] if file.startswith("internal/store/") else ["./cmd/openmerge"] if file.startswith("cmd/") else ["./internal/audit"]
        test = measured(["go", "test", "-overlay", str(overlay), "-short", "-count=1", "-timeout=2m", *packages, "-run", pattern + "|TestLandingEvidence|TestLandingDecisionEvidence"], ROOT, out, "mutation-" + name, check=False)
        output = test.stdout + test.stderr
        if "build failed" in output or "[setup failed]" in output:
            outcome = "build_or_setup_failure"
        elif test.returncode == 0:
            outcome = "equivalent" if name in equivalent else "survived"
        elif "panic: test timed out" in output or "fatal error:" in output:
            outcome = "infrastructure_failure"
        elif re.search(r"(?m)^panic:", output):
            outcome = "panic_kill"
        elif "--- FAIL:" in output:
            outcome = "assertion_kill"
        else:
            outcome = "infrastructure_failure"
        results.append({"mutation": name, "outcome": outcome, "pattern": pattern,
                        "failed_tests": re.findall(r"^--- FAIL: (\S+)", output, re.MULTILINE)})
        (out / "mutation-results.json").write_text(json.dumps(results, indent=2) + "\n")
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--phase", choices=("all", "regression"), default="all")
    parser.add_argument("--stress-pattern", default="TestGoverningPin|TestCheckAttempt|TestEvidenceAuthorityRequiredAtDecisionBoundary|TestEvidenceApprovalFamiliesAndRequiredMissing|TestRound3")
    parser.add_argument("--baseline-timing-log", type=Path, help="reuse an already completed identical baseline timing; requires adjacent exit-status.txt containing 0")
    args = parser.parse_args()
    # Guard before any subprocess; do not propagate a caller-controlled libpq route.
    maintenance = scratch_url(os.environ.get("OPENMERGE_TEST_DATABASE_URL", ""))
    os.environ["OPENMERGE_TEST_DATABASE_URL"] = maintenance
    toolchain = "/home/riels/.local/share/mise/installs/go/1.27.3"
    if os.environ.get("GOROOT") != toolchain or os.environ["PATH"].split(":")[0] != toolchain + "/bin":
        raise ValueError("export pinned Go GOROOT and PATH")
    os.environ["GOMAXPROCS"] = "2"
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    v = module("foundation_evidence", ROOT / "scripts/adr-0008-1-verify.py")
    previous = module("projection_evidence", ROOT / "scripts/verify-adr0008-9c-3.py")
    invoke = v.invoke

    def measured(cmd, cwd, work, name, **kwargs):
        print(name, flush=True)
        start = time.monotonic()
        result = invoke(cmd, cwd, work, name, **kwargs)
        v.COMMANDS[-1]["wall_seconds_including_lock"] = round(time.monotonic() - start, 3)
        v.COMMANDS[-1]["package_seconds"] = dict(re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", result.stdout, re.MULTILINE))
        return result

    v.invoke = measured
    result = {"slice": SLICE, "classification": CLASSIFICATION, "commands": v.COMMANDS}
    try:
        result["baseline"] = v.check_tree("baseline", args.baseline, v.git(args.baseline, "rev-parse", BASE))
        result["candidate"] = v.check_tree("candidate", ROOT)
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, ROOT)
        # Compare every existing fixture, not only registered wire JSON.
        fixtures = [p for p in v.git(args.baseline, "ls-files").splitlines() if "/testdata/" in p and not p.startswith("internal/audit/testdata/")]
        for path in fixtures:
            if (args.baseline / path).read_bytes() != (ROOT / path).read_bytes():
                raise ValueError("existing fixture changed")
        result["existing_fixtures_unchanged"] = len(fixtures)
        changed = v.git(ROOT, "diff", "--name-only", BASE, "HEAD").splitlines()
        if any(not (p.startswith("internal/audit/") or p.startswith("docs/") or p.startswith("scripts/") or p in ("README.md","cmd/openmerge/audit.go", "cmd/openmerge/audit_test.go", "cmd/openmerge/main.go", "cmd/openmerge/main_test.go")) for p in changed):
            raise ValueError("change outside approved audit/tests/docs/scripts scope")
        if EXTRA_PACKAGES:
            result["changed_lines"] = sum(int(n) for line in v.git(ROOT, "diff", "--numstat", BASE, "HEAD").splitlines() for n in line.split("\t")[:2])
            if result["changed_lines"] > 3500:
                raise ValueError("3500 changed-line round-1 scope gate exceeded (size reported before editing)")
            main_before = (args.baseline / "cmd/openmerge/main.go").read_text()
            main_after = (ROOT / "cmd/openmerge/main.go").read_text()
            entry = "  audit export [options]  Export recorded audit evidence\n"
            dispatch = '\tcase len(args) > 0 && args[0] == "audit":\n\t\treturn runAudit(args[1:], stdout, stderr)\n'
            if main_after.replace(entry, "", 1).replace(dispatch, "", 1) != main_before:
                raise ValueError("main.go changed beyond additive audit wiring")
            result["additive_help_dispatch_diff"] = v.git(ROOT, "diff", BASE, "HEAD", "--", "cmd/openmerge/main.go", "cmd/openmerge/main_test.go")
        result["source_sha256"] = {p: v.sha(ROOT / p) for p in changed}
        callers = [p for p in v.git(args.baseline, "ls-files", "internal/store/*test.go").splitlines()
                   if "LandingDecisionEvidence" in (args.baseline / p).read_text()]
        for path in callers:
            if (args.baseline / path).read_bytes() != (ROOT / path).read_bytes():
                raise ValueError("existing LandingDecisionEvidence caller test changed")
        result["unchanged_landing_caller_test_files"] = callers
        measured(["go", "test", "-race", "-count=1", "./internal/store", "-run", "Landing|Decision"], ROOT, out, "existing-landing-caller")
        # Both authority ports are parsed without connecting to CI's endpoint.
        # Database integration remains on the explicitly guarded caller endpoint.
        if ":5432/" not in scratch_url("postgres://postgres@localhost:5432/postgres?sslmode=disable", environ={"GITHUB_ACTIONS": "true", "CI": "true"}):
            raise ValueError("CI scratch endpoint rejected")
        measured(["go", "test", "-count=1", "./internal/audit", "-run", "^TestSourceDatabaseURLGuardAllowsLocalhostPorts$"], ROOT, out, "ci-dsn-guard")
        result["ci_dsn_guard"] = {"ports": [5432, 54329], "connection_free": True}
        if args.phase != "regression":
            result["mutations"] = mutation_proofs(measured, out, "TestGoverningPin|TestEvidence|TestClosure|TestCheckAttempt|TestRound3|TestReplay|TestValid|TestSpool|TestSourceLess|TestNormalize|TestSourceReplayRejectsScopeShapeAndCrossQueueOrder|TestExportV1SemanticExpectations")
            outcomes = [m["outcome"] for m in result["mutations"]]
            result["mutation_counts"] = {k: outcomes.count(k) for k in ("assertion_kill", "panic_kill", "equivalent", "unreachable", "survived", "build_or_setup_failure", "infrastructure_failure")}
            if any(result["mutation_counts"][k] for k in ("survived", "build_or_setup_failure", "infrastructure_failure")):
                raise ValueError("mutation verification incomplete; inspect separate outcomes")
            measured(["make", "check"], ROOT, out, "make-check")
            measured(["go", "test", "-race", "-v", "-count=1", "./internal/audit", *EXTRA_PACKAGES], ROOT, out, "evidence-full")
            burners = []
            try:
                for _ in range(8):
                    burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
                before_cpu = {b.pid: int(Path(f"/proc/{b.pid}/stat").read_text().split()[13]) for b in burners}
                stress = measured(["go", "test", "-race", "-count=20", "-v", "-timeout=240m", "./internal/audit", *EXTRA_PACKAGES, "-run", args.stress_pattern], ROOT, out, "evidence-stress", env_extra={"GOMAXPROCS": "1"})
                passed = re.findall(r"^--- PASS: (\S+)", stress.stdout, re.MULTILINE)
                if not passed or any(passed.count(name) != 20 for name in set(passed)):
                    raise ValueError("stress did not execute each selected test 20 times")
                cpu_ticks = {b.pid: int(Path(f"/proc/{b.pid}/stat").read_text().split()[13]) - before_cpu[b.pid] for b in burners}
                if any(ticks <= 0 for ticks in cpu_ticks.values()):
                    raise ValueError("CPU burner failed to consume CPU during stress")
                if any(b.poll() is not None for b in burners):
                    raise ValueError("CPU burner exited during stress")
                result["stress"] = {"iterations": 20, "GOMAXPROCS": 1, "race": True, "new_and_changed_tests": True, "non_short_included": True, "owned_burners": [b.pid for b in burners], "burner_cpu_ticks": cpu_ticks, "passed_tests": sorted(set(passed))}
            finally:
                for b in burners:
                    b.terminate()
                for b in burners:
                    b.wait()
            result["stress"]["all_reaped"] = True
            for label, tree in (("before", args.baseline), ("after", ROOT)):
                if label == "before" and args.baseline_timing_log:
                    log = args.baseline_timing_log.resolve()
                    if log.with_name("exit-status.txt").read_text().strip() != "0":
                        raise ValueError("baseline timing did not pass")
                    package_seconds = dict(re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", log.read_text(), re.MULTILINE))
                    if not package_seconds or "FAIL" in log.read_text():
                        raise ValueError("baseline timing log incomplete")
                    result["baseline_timing_reused"] = {"log": str(log), "sha256": v.sha(log), "package_seconds": package_seconds, "exit": 0}
                else:
                    measured(["go", "test", "-race", "-short", "-count=1", "-p=1", "-timeout=15m", "./..."], tree, out, "timing-" + label, env_extra={"GOMAXPROCS": "2"})
        projections, cli, mixed, contracts = [], [], [], []
        for label, tree in (("baseline", args.baseline), ("candidate", ROOT)):
            work = out / label
            work.mkdir(exist_ok=True)
            def overlay(path, text):
                source = work / (Path(path).name + ".overlay")
                source.write_text(text)
                file = work / (Path(path).stem + "-overlay.json")
                file.write_text(json.dumps({"Replace": {str(tree / path): str(source)}}))
                return str(file)
            projection = work / "projections.json"
            projections.append(projection)
            ov = overlay("internal/serve/authority_github_compatibility_test.go", (ROOT / "internal/serve/authority_github_compatibility_test.go").read_text())
            measured(["go", "test", "-overlay", ov, "./internal/serve", "-run", "^TestAuthorityGithubProjectionBytes$", "-count=1"], tree, out, label + "-projections", env_extra={"ADR_GITHUB_PROJECTIONS": projection})
            output = work / "cli.txt"
            cli.append(output)
            ov = overlay("cmd/openmerge/adr_2d_compatibility_test.go", previous.CLI_GO)
            measured(["go", "test", "-overlay", ov, "./cmd/openmerge", "-run", "^TestADR2dGithubCLI$", "-count=1"], tree, out, label + "-cli", env_extra={"ADR_GITHUB_PROJECTIONS": projection, "ADR_GITHUB_CLI": output})
            output = work / "mixed.txt"
            mixed.append(output)
            ov = overlay("internal/serve/authority_acceptance_compat_test.go", (ROOT / "internal/serve/authority_acceptance_compat_test.go").read_text())
            measured(["go", "test", "-overlay", ov, "./internal/serve", "-run", "^TestAuthorityAcceptanceMixedGithubBytes$", "-count=1"], tree, out, label + "-mixed", env_extra={"ADR_ACCEPTANCE_GITHUB_BYTES": output})
            measured(["go", "test", "-race", "-short", "-count=1", "./internal/serve", "./internal/config", "-run", "StageAAccountableIdentityDiagnosticExact|LegacyLineZeroDiagnosticsRemainExact"], tree, out, label + "-stage-a")
            output = work / "contracts.json"
            contracts.append(output)
            ov = overlay(v.TARGET, v.DIFF_GO)
            measured(["go", "test", "-overlay", ov, "./internal/queue", "-run", "^TestADR00081Differential$", "-count=1"], tree, out, label + "-contracts", env_extra={"ADR_DIFF": output})
        for name, paths in (("github_projections", projections), ("github_cli", cli), ("mixed_github", mixed), ("contracts", contracts)):
            if paths[0].read_bytes() != paths[1].read_bytes():
                raise ValueError(name + " differs")
            result[name] = {"identical": True, "sha256": v.sha(paths[1])}
        if result["mixed_github"]["sha256"] != "cc95a654054e61e816120f2e57b9e19ffea3ff4855c32f4f5c055c4e99b913ee":
            raise ValueError("mixed GitHub hash changed")
        result["contracts"]["records"] = len(json.loads(contracts[1].read_text()))
        if result["contracts"]["records"] != 90:
            raise ValueError("expected 90 contract records")
        measured([sys.executable, str(ROOT / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(ROOT), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], ROOT, out, "strict-replay", mem="6GiB")
        replay = json.loads((out / "replay.json").read_text())
        result["replay"] = {k: replay[k] for k in ("verdict", "actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical", "boundary_probe_count", "non_allowlisted_fields", "gate2", "gate4", "self_test_exit_status")}
        if replay["verdict"] != "PASS" or any(replay[k] != "2159/2159" for k in ("actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical")):
            raise ValueError("strict replay failed or unexpected checkpoint count")
        result["simulator"] = v.compare_simulator(args.baseline, ROOT, out)
        if result["simulator"]["blocks"] != 146:
            raise ValueError("expected 146 simulator blocks")
        result["verdict"] = "PASS"
    except Exception as error:
        result["verdict"] = "FAIL"
        # Failure detail is intentionally local; never echo database exceptions/DSNs.
        result["error_class"] = type(error).__name__
        print("verification failed; inspect private logs", file=sys.stderr)
    (out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    manifest = {str(p.relative_to(out)): v.sha(p) for p in sorted(out.rglob("*")) if p.is_file() and p.name != "manifest.json"}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return result["verdict"] != "PASS"


if __name__ == "__main__":
    sys.exit(main())
