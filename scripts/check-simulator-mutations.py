#!/usr/bin/env python3
"""Replay reviewer mutations through Go overlays without editing the checkout.

Run with `python3 scripts/check-simulator-mutations.py`. The regular Go suite
also tests the fence and content mutants through test-side wrappers.
"""

import json
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PLAN = "internal/queue/plan.go"
APPLY = "internal/queue/apply.go"
EXECUTOR = "internal/executor/executor.go"
FAKE = "internal/forge/fake/fake.go"


def mutation(path, *edits):
    source = (ROOT / path).read_text()
    for old, new in edits:
        if source.count(old) != 1:
            raise ValueError(f"{path}: mutation site must occur once: {old!r}")
        source = source.replace(old, new)
    return {path: source}


LAND_SHA = "Base: x.Base, ExpectedBaseSHA: a.ExpectedBaseSHA, TestedSHA: a.TestedSHA,"
ABANDON = "return emit(queue.LandingAbandoned{BatchID: a.BatchID, NewFence: next})"
MUTANTS = {
    "drop_passing_subbatch_pr": mutation(
        APPLY,
        ("s.Active.checksPassed = true",
         "if s.Active.FromBisection && len(s.Active.PRs) > 1 { s.Active.PRs = s.Active.PRs[1:] }\n\t\ts.Active.checksPassed = true"),
    ),
    "dequeue_whole_failing_batch": mutation(
        APPLY,
        ("if len(prs) == 1 {", "if true {"),
        ("s.requestDequeue(prs[0].PR, reason)", "for _, pr := range prs { s.requestDequeue(pr.PR, reason) }"),
    ),
    "no_ci_gate": mutation(PLAN, ("if batch.checksPassed {", "if true {")),
    "wrong_sha": mutation(
        EXECUTOR, (LAND_SHA, LAND_SHA.replace("TestedSHA: a.TestedSHA", "TestedSHA: a.PRs[0].HeadSHA"))
    ),
    "noop_sha": mutation(
        EXECUTOR, (LAND_SHA, LAND_SHA.replace("TestedSHA: a.TestedSHA", "TestedSHA: a.ExpectedBaseSHA"))
    ),
    "skip_trust": {
        **mutation(APPLY, ("case !e.Trusted:", "case false:"), ("case !observation.Trusted:", "case false:"), (" || !observation.Trusted", "")),
        **mutation(PLAN, (" || !observation.Trusted", "")),
    },
    "no_dequeued": mutation(
        EXECUTOR,
        ("return emit(queue.Dequeued{PR: a.PR, AdmissionID: a.AdmissionID, Reason: a.Reason, BatchID: a.BatchID})", "return nil"),
    ),
    "ignore_resolve": mutation(
        EXECUTOR,
        ("case queue.ResolveLanding:\n\t\treturn x.resolve(ctx, a, emit)", "case queue.ResolveLanding:\n\t\treturn nil"),
    ),
    "refresh_fence": mutation(
        EXECUTOR,
        ("Fence: x.fenceRef(), ExpectedFence: a.FenceToken,", "Fence: x.fenceRef(), ExpectedFence: x.freshFenceMutant(ctx),"),
        ("func (x *Executor) fenceRef() string", "func (x *Executor) freshFenceMutant(ctx context.Context) string {\n\tsha, _, _, _ := x.Git.ReadFence(ctx, x.Repo, x.fenceRef())\n\treturn sha\n}\n\nfunc (x *Executor) fenceRef() string"),
    ),
    "ignore_fence_lease": mutation(
        FAKE, (" || !f.disableFenceLeaseForTesting && r.refs[req.Fence] != req.ExpectedFence", "")
    ),
    "no_remove_landed": mutation(
        APPLY, ("\t\t\t\ts.removeEntry(pr.PR)", "\t\t\t\t// mutant leaves the landed admission queued")
    ),
    "ignore_abandon_error": mutation(
        EXECUTOR,
        (f"if err == nil {{\n\t\t\t{ABANDON}\n\t\t}}", f"if true {{\n\t\t\t{ABANDON}\n\t\t}}"),
    ),
    "ignore_base_lease": mutation(
        FAKE, ("r.refs[req.Base] != req.ExpectedBaseSHA || ", "")
    ),
    "omit_squash_pr": mutation(
        FAKE,
        ("\tfor i, pr := range req.PRs {",
         "\tbuildPRs := req.PRs\n\tif req.Method == forge.Squash { buildPRs = buildPRs[:len(buildPRs)-1] }\n\tfor i, pr := range buildPRs {"),
    ),
    "resolve_base_equality": mutation(
        EXECUTOR,
        ('\t\tswitch {\n\t\tcase id == "land/"+a.AttemptID && tested == a.TestedSHA:',
         '\t\tbase, baseErr := x.Git.ReadRef(ctx, x.Repo, x.Base)\n\t\tif baseErr != nil { return baseErr }\n\t\tswitch {\n\t\tcase base == a.TestedSHA:'),
    ),
    "omit_pr_files": mutation(
        FAKE, ("\t\t\t\tfiles[path] = value", "\t\t\t\tif i > 0 { files[path] = value }"),
    ),
    "omit_merge_parent": mutation(
        FAKE, ("r.commit([]string{top, pr.HeadSHA}, content, message, files)",
               "r.commit([]string{top}, content, message, files)"),
    ),
}

# A failed test must report a simulator error, rather than merely an incorrect
# aggregate returned by an otherwise successful Run.
PROBE = '''package sim
import (
    "testing"
    "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestMutationRunProbe(t *testing.T) {
    for _, mode := range []string{"passing", "broken", "squash", "external", "batching"} {
        t.Run(mode, func(t *testing.T) {
            cfg := smallConfig()
            cfg.PRs, cfg.MaxSteps, cfg.Faults = 2, 10000, DefaultFaults()
            if mode == "broken" { cfg.BrokenRate = 1 }
            if mode == "squash" { cfg.MergeMethod = queue.Squash }
            if mode == "external" { cfg.ExternalPushRate = 1 }
            if mode == "batching" {
                cfg.PRs, cfg.BrokenRate, cfg.Faults = 16, 0.2, Faults{}
                cfg.ArrivalWindow = 0
                cfg.Planner.BatchMaxSize = 8
            }
            r, err := Run(cfg)
            if err != nil { t.Fatalf("simulator rejected mutation: %v; merged=%d", err, r.Merged) }
        })
    }
}
'''
EVIDENCE = ("simulator rejected mutation:", "abandoned push accepted:", "wedge:")
report = {}
with tempfile.TemporaryDirectory(prefix="openmerge-mutations-") as directory:
    for name, changes in {"control": {}, **MUTANTS}.items():
        folder = Path(directory) / name
        folder.mkdir()
        replacements = {}
        for index, (path, source) in enumerate(changes.items()):
            file = folder / f"{index}.go"
            file.write_text(source)
            replacements[str(ROOT / path)] = str(file)
        probe = folder / "probe_test.go"
        probe.write_text(PROBE)
        replacements[str(ROOT / "internal/sim/mutation_probe_test.go")] = str(probe)
        overlay = folder / "overlay.json"
        overlay.write_text(json.dumps({"Replace": replacements}))
        run = subprocess.run(
            ["go", "test", "-count=1", "-overlay", str(overlay), "./internal/sim", "-run",
             "TestMutationRunProbe|TestAbandonmentBeforePush/captured_token|TestExit500PRDay|TestForeignBaseEqualityDoesNotResolveLanding/resolve_from_fence"],
            cwd=ROOT, capture_output=True, text=True, timeout=60,
        )
        output = run.stdout + run.stderr
        if name == "control":
            if run.returncode != 0:
                raise SystemExit(f"unmutated simulator failed the mutation probes:\n{output}")
            print("control: passed", flush=True)
            continue
        evidence = next((line.strip() for line in output.splitlines()
                         if any(marker in line for marker in EVIDENCE)), "")
        if run.returncode == 0 or "build failed" in output or not evidence:
            report[name] = {"status": "missed", "output": output}
        else:
            report[name] = {"status": "detected", "evidence": evidence.split("; result=", 1)[0]}
        print(f"{name}: {report[name]['status']}", flush=True)
print(json.dumps(report, indent=2))
missed = sum(item["status"] == "missed" for item in report.values())
print(f"{len(report) - missed} detected, {missed} missed", flush=True)
raise SystemExit(1 if missed else 0)
