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
TYPES = "internal/queue/types.go"
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
    "window_does_not_halve": mutation(
        "internal/queue/adaptive.go", ("s.window.Current/2", "s.window.Current")
    ),
    "window_does_not_grow": mutation(
        "internal/queue/adaptive.go", ("s.window.Current+1", "s.window.Current")
    ),
    "halves_halve_window_again": mutation(
        "internal/queue/adaptive.go", ("b.FromBisection || b.windowFailureRecorded", "b.windowFailureRecorded")
    ),
    "ignore_adaptive_formation": mutation(
        PLAN, ("size := s.BatchSize(cfg)", "size := max(1,cfg.BatchMaxSize)")
    ),
    "ignore_adaptive_size": mutation(
        "internal/queue/adaptive.go", ("if target == 0 || samples < 20 {", "if true {")
    ),
    "adaptive_equality_rejected": mutation(
        "internal/queue/adaptive.go", ("if left.Cmp(right) < 0 {", "if left.Cmp(right) <= 0 {")
    ),
    "outcome_ring_too_short": mutation(
        "internal/queue/adaptive.go", ("len(s.outcomes)-200:", "len(s.outcomes)-199:")
    ),
    "cancel_landing_or_landed_batch": mutation(
        "internal/queue/check_cancellation.go",
        ("!b.Speculative || b.Phase == Landing || b.TestedSHA", "!b.Speculative || b.TestedSHA"),
    ),
    "out_of_order_landing": mutation(
        PLAN, ("if batch != s.Active || batch.BaseSHA != s.BaseSHA {", "if false {")
    ),
    "blame_provisional_failure": mutation(
        APPLY, ("if b.Speculative && b != s.Active {\n\t\tb.ProvisionalFailure", "if false {\n\t\tb.ProvisionalFailure")
    ),
    "ignore_adaptive_window": mutation(
        "internal/queue/chain.go", ("len(s.Batches()) >= s.Window(cfg)", "false")
    ),
    "chain_built_on_wrong_commit": mutation(
        APPLY, ("b.LandingSHA = e.TestedSHA", "b.LandingSHA = b.BaseSHA")
    ),
    "uncorrelated_batch_timeout": mutation(
        APPLY,
        ("e.Reason == ChecksTimeout && s.batchIn(e.BatchID, Testing) && (!s.Batch(e.BatchID).checksPassed || s.Batch(e.BatchID).ProvisionalFailure != \"\")",
         "e.Reason == ChecksTimeout && s.Active != nil && s.Active.Phase == Testing && (!s.Active.checksPassed || s.Active.ProvisionalFailure != \"\")"),
        ("s.failBatch(s.Batch(e.BatchID), e.Reason)", "s.failBatch(s.Active, e.Reason)"),
    ),
    "build_key_omits_composition": mutation(
        TYPES,
        ("for _, pr := range a.PRs {", "for _, pr := range a.PRs[:0] {"),
    ),
    "ignore_batch_max_wait": mutation(
        PLAN,
        ("if len(prs) > 0 && size > 1 && cfg.BatchMaxWait > 0 {",
         "if len(prs) > 0 && size > 1 && false {"),
    ),
    "drop_passing_subbatch_pr": mutation(
        APPLY,
        ("b.checksPassed = true",
         "if b.FromBisection && len(b.PRs) > 1 { b.PRs = b.PRs[1:] }\n\t\tb.checksPassed = true"),
    ),
    "dequeue_whole_failing_batch": mutation(
        APPLY,
        ("if len(prs) == 1 {", "if true {"),
        ("if true {\n\t\ts.requestDequeue(prs[0].PR, reason)", "if true {\n\t\tfor _, pr := range prs { s.requestDequeue(pr.PR, reason) }"),
    ),
    "no_ci_gate": mutation(PLAN, ('if batch.checksPassed && batch.ProvisionalFailure == "" {', "if true {")),
    "land_ignores_generation": mutation(
        PLAN,
        ("ChecksGeneration: batch.checksGeneration,", "ChecksGeneration: 0,"),
    ),
    "reopen_does_not_clear_success": mutation(
        APPLY,
        ("b.checksGeneration++\n\t\t\tb.checksPassed = false", "b.checksGeneration++ // mutant preserves success after a reopen"),
    ),
    "no_landing_ci_guard": mutation(
        EXECUTOR,
        ("if !terminal || !success {", "if (!terminal || !success) && false {"),
    ),
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
        FAKE, ("\t\t\t\tparents = append(parents, pr.HeadSHA)\n",
               "\t\t\t\t// mutant omits the PR head parent\n"),
    ),
}

# A failed test must report a simulator error, rather than merely an incorrect
# aggregate returned by an otherwise successful Run.
PROBE = '''package sim
import (
    "testing"
    "time"
    "strings"
    "fmt"
    "github.com/CanadaApollo6/openmerge/internal/queue"
)
func TestMutationRunProbe(t *testing.T) {
    for _, mode := range []string{"passing", "broken", "squash", "external", "batching", "ci-rerun", "chain", "chain-broken", "chain-timeout", "adaptive", "adaptive-boundary"} {
        t.Run(mode, func(t *testing.T) {
            cfg := smallConfig()
            cfg.PRs, cfg.MaxSteps, cfg.Faults = 2, 10000, DefaultFaults()
            if mode == "ci-rerun" { cfg.Faults = Faults{CIRerunRate: 1} }
            if mode == "broken" { cfg.BrokenRate = 1 }
            if mode == "squash" { cfg.MergeMethod = queue.Squash }
            if mode == "external" { cfg.ExternalPushRate = 1 }
            if mode == "batching" {
                cfg.PRs, cfg.BrokenRate, cfg.Faults = 16, 0.2, Faults{}
                cfg.ArrivalWindow = 0
                cfg.Planner.BatchMaxSize = 8
            }
            if mode == "chain" || mode == "chain-broken" || mode == "chain-timeout" {
                cfg.PRs,cfg.ArrivalWindow,cfg.Faults = 16,0,Faults{}
                cfg.Planner.SpeculationMaxDepth=4
                cfg.Planner.ChecksRetryFailed=1
                cfg.CI=&CIConfig{Runners:50,RerunsSupported:true,CancellationSupported:true}
                cfg.Planner.BatchMaxSize=1
                cfg.CIMin,cfg.CIMax=time.Minute,3*time.Minute
                cfg.BrokenRate=0
                if mode == "chain-broken" { cfg.BrokenRate=.5 }
                if mode == "chain-timeout" { cfg.Planner.CheckTimeout=90*time.Second }
            }
            if mode == "adaptive" || mode == "adaptive-boundary" {
                cfg.PRs,cfg.ArrivalWindow,cfg.Faults = 256,0,Faults{}
                cfg.Planner.SpeculationMaxDepth=4
                cfg.CI=&CIConfig{Runners:50,RerunsSupported:true,CancellationSupported:true}
                cfg.Planner.BatchMaxSize=8
                cfg.Planner.BatchTargetPassRate=.8
                cfg.BrokenRate=.1
            }
            if mode == "adaptive-boundary" {
                cfg.Planner.BatchTargetPassRate=.81
                sim,err:=newSimulation(cfg)
                if err!=nil { t.Fatal(err) }
                sim.record(sim.cfg.Planner.Applied("boundary",strings.Repeat("a",64)))
                sim.cfg.Planner.ConfigDigest=strings.Repeat("a",64)
                for pr:=1;pr<=20;pr++ {
                    sim.record(queue.PRObserved{PR:pr,HeadSHA:"h",Eligible:true,Trusted:true})
                    sim.record(queue.EnqueueRequested{PR:pr,At:sim.now})
                    id:=fmt.Sprintf("b%d",pr)
                    sim.record(queue.ChainedBatchStarted{BatchID:id,BaseSHA:sim.state.BaseSHA,PRs:[]queue.BatchPR{{PR:pr,HeadSHA:"h"}},At:sim.now,Position:1})
                    sha:=fmt.Sprintf("sha%d",pr)
                    sim.record(queue.ChainedTestRefBuilt{BatchID:id,TestedSHA:sha,LandingSHA:sha,Method:queue.Merge,At:sim.now,Position:1})
                    if pr<=2 {
                        sim.record(queue.ChecksFailureConfirmed{BatchID:id,TestedSHA:sha,Reason:queue.ChecksFailed})
                        sim.record(queue.Dequeued{PR:pr,AdmissionID:uint64(pr),Reason:queue.ChecksFailed})
                    } else {
                        sim.record(queue.ChecksCompleted{TestedSHA:sha,Success:true})
                        sim.record(queue.LandingStarted{BatchID:id,AttemptID:id,FenceToken:sim.state.Fence,At:sim.now})
                        // Fold directly: these test commits have no fake git graph.
                        before:=sim.state
                        e:=queue.Landed{BatchID:id,NewBaseSHA:sha,NewFence:"fence"}
                        sim.state=queue.Apply(sim.state,e)
                        sim.observeAdaptation(before,e)
                    }
                }
                if err:=sim.checkAdaptiveInvariants();err!=nil { t.Fatalf("simulator rejected mutation: %v",err) }
                return
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
             "TestMutationRunProbe|TestProvisionalConflictInSimulator|TestDelayedPassCannotReviveLandingInSimulator|TestStaleBatchTimeoutInSimulator|TestChangedCompositionInSimulator|TestBatchFormationWaitInSimulator|TestAbandonmentBeforePush/captured_token|TestExit500PRDay|TestForeignBaseEqualityDoesNotResolveLanding/resolve_from_fence"],
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
