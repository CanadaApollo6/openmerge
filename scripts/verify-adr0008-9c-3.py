#!/usr/bin/env python3
"""Rerun the offline PR 9c-3 Hold evidence against a clean baseline checkout."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import uuid
import sys
import re
import time
sys.dont_write_bytecode = True
from adr0008_scratch_db import scratch_url


CLI_GO = r'''package main
import (
 "bytes"
 "encoding/json"
 "os"
 "testing"
 "github.com/CanadaApollo6/openmerge/internal/status"
)
func TestADR2dGithubCLI(t *testing.T) {
 raw,err:=os.ReadFile(os.Getenv("ADR_GITHUB_PROJECTIONS"));if err!=nil {t.Fatal(err)}
 var projections map[string]string
 if err=json.Unmarshal(raw,&projections);err!=nil {t.Fatal(err)}
 var response status.Response
 if err=json.Unmarshal([]byte(projections["/v1/queues"]),&response);err!=nil {t.Fatal(err)}
 var output bytes.Buffer
 if err=printStatus(&output,response);err!=nil {t.Fatal(err)}
 if err=os.WriteFile(os.Getenv("ADR_GITHUB_CLI"),output.Bytes(),0600);err!=nil {t.Fatal(err)}
}
'''


def scratch_sql(url, sql):
    result = subprocess.run(["psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", url],
                            input=sql, text=True, capture_output=True, timeout=120)
    if result.returncode:
        raise RuntimeError("scratch database operation failed")


STRESS_GROUPS = (
    ("stress-public-serve", ["./internal/serve"], "^TestPublicAuthority|^TestReviewPublicInventoryErrorClass$|^TestAuthorityActivationReceiptBeforeIO$|^TestAuthorityActivationReceiptExpiresDuringSeeingRead$|^TestAuthorityLifecycleRollbackReceiptAndUnchangedWire$|^TestAuthorityBarrierJournalCannotInstallRuntimeAuthority$|^TestAuthorityLifecycleRequiresFlagAndResolver$"),
    ("stress-public-base-recovery", ["./internal/serve"], "^TestAuthorityAcceptanceBaseAdvanceBeforeBuild$"),
    ("stress-public-config-store-cli-gitops", ["./internal/config", "./internal/changepolicy", "./internal/store", "./cmd/openmerge", "./internal/gitops"], "TestFullCandidateSchemaComposition|TestAuthorityPublicAgreement|TestAuthorityCandidate|TestConfigValidateAuthorityPublicCorpus|TestPublicAuthority|TestAuthorityEmissionLiveBackup|TestBuildCreationBoundaryRejectsUnprovenSources|TestSetupImportGoverningEvidenceCannotBeAccepted|TestAuthorityAcceptanceImportGoverningEvidenceCannotBeAccepted|TestReviewFirstEmissionAppendRace|TestAuthorityEmissionReceiptBoundary|TestAuthorityEmissionSerializesAllQueueAppends|TestAuthorityEmissionRefreshesSnapshotAfterAppendLockWait|TestVerificationReceiptOnlyBeforeBoundary|TestVerificationReceiptValidationSanitizedBeforeSeeingRead|TestBuildBaseProofPreservesFailureClasses|TestAuthorityCompletionDedicatedCacheAvoidsSessionLock|TestProductionAuthorityRuntimeWiring|TestHistoricalStageAAuthoritySchemaComposition|TestReview4AuditPreservesWorkerQuota"),
)


def run_stress(v, root, out, groups=STRESS_GROUPS):
    """Bound each package invocation while retaining 20 loaded runs per test."""
    print("20-iteration race stress in bounded groups with eight owned CPU burners", flush=True)
    burners = []
    try:
        for _ in range(8):
            burners.append(subprocess.Popen([sys.executable, "-c", "while True: pass"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
        for name, packages, pattern in groups:
            print(name, flush=True)
            v.invoke(["go", "test", "-p=1", "-race", "-short", "-cpu=1", "-count=20", "-timeout=15m", *packages, "-run", pattern], root, out, name, env_extra={"GOMAXPROCS": "1"})
    finally:
        for burner in burners:
            burner.terminate()
        for burner in burners:
            burner.wait()
    return {"iterations": 20, "GOMAXPROCS": 1, "race": True, "owned_cpu_burners": len(burners), "all_reaped": True, "groups": [name for name, _, _ in groups]}


def verify_consult_mutations(v, root, out):
    """Every bypass must fail a named behavioral test, never merely fail to build."""
    cases = [
        ("public-reader-error-class", "internal/serve/authority_public.go", "./internal/serve", "TestPublicAuthorityReaderPreservesSanitizedErrorClasses",
         lambda s: s.replace("return evidence, &authorityObservationReadFailure{err: err}", "return evidence, errors.New(\"authority protection read unavailable\")", 1)),
        ("public-github-loader", "internal/serve/reconcile.go", "./internal/serve", "TestPublicAuthorityGithubConfigurationBytes",
         lambda s: s.replace("candidate.Config, warnings, err = config.Load(data)", "candidate, warnings, err = config.LoadAuthorityCandidate(data)", 1)),
        ("setup-live-backup-call", "cmd/openmerge/setup_verify.go", "./cmd/openmerge", "TestPublicAuthoritySetupVerifyRequiresLiveBackupWatermark",
         lambda s: s.replace("if err := db.CheckAuthorityEmissionBackup(ctx, receipt.Reconciliation.BackupWatermark); err != nil {", "if err := error(nil); err != nil {", 1)),
        ("typed-base-move", "internal/gitops/prepared_push.go", "./internal/serve", "TestAuthorityAcceptanceBaseAdvanceBeforeBuild",
         lambda s: s.replace("return &forge.BaseMovedError{SHA: live}", "return errors.New(\"untyped base failure\")", 1)),
        ("final-governing-recheck", "internal/serve/authority_activation.go", "./internal/serve", "TestPublicAuthorityActivationRechecksGoverningTip",
         lambda s: s.replace("if request.Recheck != nil {", "if false {", 1)),
        ("require-complete", "internal/serve/authority_public.go", "./internal/serve", "TestPublicAuthorityActivationConsultsCompletion",
         lambda s: s.replace("return completion.RequireComplete()", "_ = completion; return nil")),
        ("repository-source", "internal/serve/authority_completion.go", "./internal/serve", "TestPublicAuthorityActivationConsultsCompletion",
         lambda s: re.sub(r"source\.RepositoryAuthorityCompletion\(ctx, request, store\.AuthorityBranchInventory\{DefaultBranch: branch, DefaultTip: sha, Branches: (?:before|after)\}\)", "func() (store.AuthorityCompletion,error) { return store.AuthorityCompletion{Request:request,Complete:true,Queues:[]store.AuthorityQueueCompletion{{Reason:\"complete\"}}},nil }()", s)),
        ("pre-switch-verification", "internal/serve/authority_public.go", "./internal/serve", "TestPublicAuthorityActivationRequiresVerification",
         lambda s: re.sub(r"if plan.DraftID <= 0 \{\s*return authorityLifecycleResolution\{\}, ErrAuthorityVerificationRequired\s*\}", "", s, count=1)),
        ("live-backup-watermark", "internal/store/authority_emission.go", "./internal/store", "TestAuthorityEmissionLiveBackupWatermark",
         lambda s: s.replace("if live != gate.Watermark {", "if false {")),
        ("production-constructor", "internal/serve/branches.go", "./internal/serve", "TestPublicAuthorityProductionConstructor",
         lambda s: s.replace("target = s.publicAuthorityTarget(target)", "")),
    ]
    # Round-1 behavioral regressions. Replace all occurrences so a second
    # defensive check cannot hide the same bypass.
    recipes = [{'name': 'first-emission-final-watermark',
      'file': 'internal/store/write_restriction.go',
      'package': './cmd/openmerge',
      'test': 'TestReviewFirstEmissionAppendRace',
      'old': 'for _, gate := range emission {',
      'new': 'for _, gate := range []AuthorityEmissionGate{} {'},
     {'name': 'first-emission-global-append-lock',
      'file': 'internal/store/authority_emission.go',
      'package': './internal/store',
      'test': 'TestAuthorityEmissionSerializesAllQueueAppends',
      'old': 'LOCK TABLE events IN SHARE ROW EXCLUSIVE MODE',
      'new': 'SELECT 1'},
     {'name': 'receipt-boundary-ignore-first',
      'file': 'internal/store/authority_emission.go',
      'package': './internal/store',
      'test': 'TestAuthorityEmissionReceiptBoundary',
      'old': 'if gate.Validate != nil {',
      'new': 'if false && gate.Validate != nil {'},
     {'name': 'receipt-boundary-require-after',
      'file': 'internal/store/authority_emission.go',
      'package': './internal/store',
      'test': 'TestAuthorityEmissionReceiptBoundary',
      'old': 'if boundary {',
      'new': 'if false && boundary {'},
     {'name': 'runtime-receipt-after-boundary',
      'file': 'internal/serve/authority_public.go',
      'package': './internal/serve',
      'test': 'TestPublicAuthorityReceiptNotRequiredAfterBoundary',
      'old': 'if !boundary {',
      'new': 'if true || !boundary {'},
     {'name': 'rollback-receipt-required',
      'file': 'internal/serve/authority_activation.go',
      'package': './internal/serve',
      'test': 'TestPublicAuthorityReceiptNotRequiredAfterBoundary',
      'old': 'needsReceipt := request.Pin.Authority != authority.GitHub && !boundary',
      'new': 'needsReceipt := request.Pin.Authority == authority.GitHub || !boundary'},
     {'name': 'setup-receipt-after-boundary',
      'file': 'cmd/openmerge/setup_verify.go',
      'package': './cmd/openmerge',
      'test': 'TestVerificationReceiptOnlyBeforeBoundary',
      'old': 'if boundary {',
      'new': 'if false && boundary {'},
     {'name': 'setup-receipt-validation-bypass',
      'file': 'cmd/openmerge/setup_verify.go',
      'package': './cmd/openmerge',
      'test': 'TestVerificationReceiptValidationSanitizedBeforeSeeingRead',
      'old': 'receipt.ValidateForBinary(identity, time.Now(), maxAge, '
             'authority.RunningBinaryRevision())',
      'new': 'error(nil)'},
     {'name': 'public-unlisted-adopts-first',
      'file': 'internal/serve/authority_public.go',
      'package': './internal/serve',
      'test': 'TestPublicAuthorityUnlistedQueueStaysGithub',
      'old': 'if intended == nil {\n\t\treturn target\n\t}',
      'new': 'if intended == nil { intended = &runtime.Targets[0] }'},
     {'name': 'reviewed-plan-binding-bypass',
      'file': 'internal/serve/authority_activation.go',
      'package': './internal/serve',
      'test': 'TestPublicAuthorityReviewedBindingMustMatch',
      'old': 'if w.publicAuthority && !reflect.DeepEqual(request.Input.Binding, binding) {',
      'new': 'if false && w.publicAuthority && !reflect.DeepEqual(request.Input.Binding, binding) {'},
     {'name': 'inventory-error-class-erasure',
      'file': 'internal/serve/authority_public.go',
      'package': './internal/serve',
      'test': 'TestReviewPublicInventoryErrorClass',
      'old': 'return authorityLifecycleResolution{}, &sanitizedAuthorityInventoryError{cause: err}',
      'new': 'return authorityLifecycleResolution{}, errors.New("authority branch inventory '
             'unavailable")'},
     {'name': 'negative-ancestry-any-error',
      'file': 'internal/gitops/prepared_push.go',
      'package': './internal/gitops',
      'test': 'TestBuildBaseProofPreservesFailureClasses',
      'old': 'live != base && exitCode(err, 1) && strings.TrimSpace(diagnostic) == ""',
      'new': 'live != base && err != nil'},
     {'name': 'negative-ancestry-diagnostic',
      'file': 'internal/gitops/prepared_push.go',
      'package': './internal/gitops',
      'test': 'TestBuildBaseProofPreservesFailureClasses',
      'old': ' && strings.TrimSpace(diagnostic) == ""',
      'new': ''},
     {'name': 'production-runtime-wiring',
      'file': 'cmd/openmerge/serve.go',
      'package': './cmd/openmerge',
      'test': 'TestProductionAuthorityRuntimeWiring',
      'old': 'Authority: authorityRuntime, ',
      'new': ''},
     {'name': 'production-app-binding',
      'file': 'cmd/openmerge/serve.go',
      'package': './cmd/openmerge',
      'test': 'TestProductionAuthorityRuntimeWiring',
      'old': 'return nil, errors.New("authority plan App binding mismatch")',
      'new': ''},
     {'name': 'production-completion-cache',
      'file': 'cmd/openmerge/installations.go',
      'package': './cmd/openmerge',
      'test': 'TestReview4AuditPreservesWorkerQuota',
      'old': 'AuthorityCompletionGit: b.authorityGit, ',
      'new': ''},
     {'name': 'runtime-first-receipt-bypass',
      'file': 'internal/serve/authority_activation.go',
      'package': './internal/serve',
      'test': 'TestAuthorityActivationReceiptBeforeIO',
      'old': 'request.Receipt.ValidateForBinary(identity, time.Now().UTC(), request.ReceiptMaxAge, '
             'authority.RunningBinaryRevision())',
      'new': 'error(nil)'}]
    recipes.append({"name": "legacy-ack-waives-authority-receipt", "file": "internal/store/authority_emission.go",
                    "package": "./internal/store", "test": "TestAuthorityEmissionLiveBackupWatermark",
                    "old": "authorityEmissionEventTypes).Scan", "new": "authorityDowngradeEventTypes).Scan"})
    recipes.append({'name': 'rollback-configured-database-binding', 'file': 'internal/serve/authority_activation.go', 'package': './internal/serve', 'test': 'TestPublicAuthorityRollbackKeepsDatabaseBinding', 'old': 'if w.publicAuthority && s.options.Authority != nil && s.options.Authority.DatabaseIdentity != "" {', 'new': 'if false && w.publicAuthority && s.options.Authority != nil && s.options.Authority.DatabaseIdentity != "" {'})
    recipes.append({'name': 'first-emission-default-isolation', 'file': 'internal/store/write_restriction.go', 'package': './internal/store', 'test': 'TestAuthorityEmissionRefreshesSnapshotAfterAppendLockWait', 'old': 'pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {', 'new': 'pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {'})
    for recipe in recipes:
        cases.append((recipe["name"], recipe["file"], recipe["package"], recipe["test"],
                      lambda source, r=recipe: source.replace(r["old"], r["new"])))
    COMPLETION_CASES = [
     ('completion-verdict-equality', 'internal/serve/authority_completion.go', './internal/serve', 'TestPublicAuthorityCompletionPartitionRace', lambda s: s.replace('if !reflect.DeepEqual(result, final) {', 'if false && !reflect.DeepEqual(result, final) {', 1)),
     ('completion-partition-race', 'internal/serve/authority_completion.go', './internal/serve', 'TestPublicAuthorityCompletionPartitionRace', lambda s: s.replace('final, err := source.RepositoryAuthorityCompletion(ctx, request, store.AuthorityBranchInventory{DefaultBranch: branch, DefaultTip: sha, Branches: after})', 'final, err := result, error(nil)', 1)),
     ('completion-discovery-error-class', 'internal/serve/authority_completion.go', './internal/serve', 'TestPublicAuthorityCompletionErrorClasses', lambda s: s.replace('return &authorityCompletionReadError{operation: operation, cause: cause}', 'return &authorityCompletionReadError{operation: operation}', 1)),
     ('completion-dispatch-duplicate', 'internal/serve/push.go', './internal/serve', 'TestPublicAuthorityCompletionRealLandCallBudget', lambda s: s.replace('DispatchGuard: dispatchGuard,', 'DispatchGuard: nil,', 1)),
    ]
    for _operation in ['PushBuild','PushLand','PushEnsureFence','PushAdvanceFence','PushDeleteBranch']:
     COMPLETION_CASES.append(('completion-consult-'+_operation,'internal/serve/push.go','./internal/serve','TestPublicAuthorityEveryPushConsultsCompletion',lambda s,op=_operation:s.replace('if w.publicAuthority {','if w.publicAuthority && operation != forge.'+op+' {',1)))
    cases.extend(COMPLETION_CASES)
    receipts = []
    for name, path, package, test, mutate in cases:
        original = root / path
        source = original.read_text()
        mutated = mutate(source)
        if source == mutated:
            raise v.VerifyError("mutation did not match source: " + name)
        work = out / ("mutation-" + name)
        work.mkdir(exist_ok=True)
        mutant = work / original.name
        mutant.write_text(mutated)
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {str(original): str(mutant)}}))
        result = v.invoke(["go", "test", "-overlay", str(overlay), "-race", "-short", "-count=1", package, "-run", "^" + test + "$"], root, out, "mutant-" + name, check=False)
        if result.returncode != 1 or "--- FAIL: " + test not in result.stdout or "[build failed]" in result.stdout:
            raise v.VerifyError("mutation was not killed by named test: " + name)
        receipts.append({"mutation": name, "path": path, "test": test, "killed": True, "exit": result.returncode})
    return receipts


def main():
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--toolchain", type=Path, default=os.environ.get("GOROOT"))
    parser.add_argument("--lock", type=Path, default=Path.home() / ".cache/openmerge-heavy.lock")
    parser.add_argument("--events", type=Path, default=Path.home() / ".cache/om-pr72-fix1-input/events.jsonl")
    parser.add_argument("--queues", type=Path, default=Path.home() / ".cache/om-pr72-fix1-input/queues.jsonl")
    parser.add_argument("--timing-before-log", type=Path, help="reuse a completed 75c4029 CI-like timing stdout log; otherwise measure it")
    args = parser.parse_args()
    maintenance = scratch_url(os.environ.get("OPENMERGE_TEST_DATABASE_URL", ""))
    # Reuse the shared guard's strictly allowlisted scratch namespace.
    database_name = "om_9c2c_" + uuid.uuid4().hex
    database_url = scratch_url(maintenance, database_name)
    created = False
    original_database = os.environ.get("OPENMERGE_TEST_DATABASE_URL")
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location("evidence", root / "scripts/adr-0008-1-verify.py")
    v = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(v)
    v.LOCK, v.EVENTS, v.QUEUES = args.lock, args.events, args.queues
    original_invoke = v.invoke
    def measured_invoke(cmd, cwd, run, name, **kwargs):
        started = time.monotonic()
        result = original_invoke(cmd, cwd, run, name, **kwargs)
        v.COMMANDS[-1]["wall_seconds"] = round(time.monotonic() - started, 3)
        v.COMMANDS[-1]["package_seconds"] = {
            package: float(seconds) for package, seconds in re.findall(
                r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", result.stdout, re.MULTILINE)}
        return result
    v.invoke = measured_invoke
    result = {"trial_class": "Hold", "slice": "9c-3", "offline_only": True, "commands": v.COMMANDS}
    try:
        if args.toolchain is None:
            raise v.VerifyError("set GOROOT or pass --toolchain for Go 1.27.3")
        toolchain = str(args.toolchain.resolve())
        if os.environ.get("GOROOT") != toolchain or os.environ["PATH"].split(":")[0] != toolchain + "/bin":
            raise v.VerifyError("export both Go 1.27.3 GOROOT and its bin directory first in PATH")
        if (args.toolchain / "VERSION").read_text().splitlines()[0] != "go1.27.3":
            raise v.VerifyError("Go 1.27.3 is required")

        result["baseline"] = v.check_tree("baseline", args.baseline)
        if not result["baseline"].startswith("75c4029"):
            raise v.VerifyError("baseline must be main 75c4029")
        result["candidate"] = v.check_tree("candidate", root)
        os.environ.setdefault("GOMAXPROCS", "2")
        result["environment"] = {"GOROOT": toolchain, "PATH_prefix": toolchain + "/bin", "database": "localhost:54329, isolated test schemas"}
        result["exports"] = v.exports(v.EVENTS, v.QUEUES)
        result["wire"] = v.compare_fixtures(args.baseline, root)
        modified_fixtures = v.git(root,"diff","--name-only","--diff-filter=M",result["baseline"],"HEAD","--","*/testdata/*")
        if modified_fixtures:
            raise v.VerifyError("existing fixtures modified: " + modified_fixtures)
        result["existing_fixtures_unchanged"] = True
        sources = [root / path for path in v.git(root,"diff","--name-only",result["baseline"],"HEAD").splitlines() if (root / path).is_file()]
        result["source_hashes"] = {str(path.relative_to(root)): v.sha(path) for path in sources}
        scratch_sql(maintenance, f'CREATE DATABASE "{database_name}"')
        created = True
        os.environ["OPENMERGE_TEST_DATABASE_URL"] = database_url
        print("make check", flush=True)
        v.invoke(["make", "check"], root, out, "make-check")
        result["consult_mutations"] = verify_consult_mutations(v, root, out)
        print("focused propagation, operations, barrier and exact-output race tests", flush=True)
        v.invoke(["go", "test", "-race", "-short", "-v", "./internal/authority", "./internal/gitops", "./internal/serve", "./internal/store", "./internal/queue", "./internal/config", "./internal/status", "./cmd/openmerge", "-run", "PublicAuthority|AuthorityAcceptance|AuthorityEmissionLiveBackup|Completion|AuthorityBarrier|LegacyLineZero|StageAAccountableIdentityDiagnosticExact", "-count=1"], root, out, "focused")
        for label, tree in (("before", args.baseline), ("after", root)):
            if label == "before" and args.timing_before_log:
                raw = args.timing_before_log.read_text()
                packages = {package: float(seconds) for package, seconds in re.findall(r"^ok\s+(\S+)\s+(\d+(?:\.\d+)?)s", raw, re.MULTILINE)}
                expected = {"github.com/CanadaApollo6/openmerge/" + p for p in ("internal/serve", "internal/gitops", "internal/queue", "cmd/openmerge")}
                if set(packages) != expected or "FAIL" in raw:
                    raise v.VerifyError("incomplete baseline package timing log")
                (out / "timing-before.log").write_text(raw)
                result["timing_before"] = {"package_seconds": packages, "source_sha256": v.sha(args.timing_before_log), "reused": True}
                continue
            print("CI-like package timing " + label, flush=True)
            v.invoke(["go", "test", "-p=1", "-race", "-short", "-count=1", "-timeout=15m", "./internal/serve", "./internal/gitops", "./internal/queue", "./cmd/openmerge"], tree, out, "timing-" + label, env_extra={"GOMAXPROCS": "2"})
        print("byte-identical GitHub worker/status/API/health and CLI projections", flush=True)
        projection_paths = []
        cli_paths = []
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / (label + "-projections")
            work.mkdir(exist_ok=True)
            source = work / "compatibility_test.go"
            source.write_text((root / "internal/serve/authority_github_compatibility_test.go").read_text())
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / "internal/serve/authority_github_compatibility_test.go"): str(source)}}))
            projection = work / "projections.json"
            projection_paths.append(projection)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/serve", "-run", "^TestAuthorityGithubProjectionBytes$", "-count=1"], tree, out, label + "-github-projections", env_extra={"ADR_GITHUB_PROJECTIONS": projection})
            cli_source = work / "cli_test.go"
            cli_source.write_text(CLI_GO)
            overlay.write_text(json.dumps({"Replace": {str(tree / "cmd/openmerge/adr_2d_compatibility_test.go"): str(cli_source)}}))
            cli = work / "cli.txt"
            cli_paths.append(cli)
            v.invoke(["go", "test", "-overlay", str(overlay), "./cmd/openmerge", "-run", "^TestADR2dGithubCLI$", "-count=1"], tree, out, label + "-github-cli", env_extra={"ADR_GITHUB_PROJECTIONS": projection, "ADR_GITHUB_CLI": cli})
        if projection_paths[0].read_bytes() != projection_paths[1].read_bytes() or cli_paths[0].read_bytes() != cli_paths[1].read_bytes():
            raise v.VerifyError("GitHub projection or CLI bytes differ")
        result["github_projections"] = {"identical": True, "status_api_health_author_sha256": v.sha(projection_paths[1]), "cli_sha256": v.sha(cli_paths[1])}
        print("same-repository mixed GitHub OIDs, permissions and event bytes", flush=True)
        mixed_paths = []
        mixed_source = (root / "internal/serve/authority_acceptance_compat_test.go").read_text()
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / (label + "-mixed")
            work.mkdir(exist_ok=True)
            source = work / "mixed_test.go"
            source.write_text(mixed_source)
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / "internal/serve/authority_acceptance_compat_test.go"): str(source)}}))
            output = work / "mixed.json"
            mixed_paths.append(output)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/serve", "-run", "^TestAuthorityAcceptanceMixedGithubBytes$", "-count=1"], tree, out, label + "-mixed-github", env_extra={"ADR_ACCEPTANCE_GITHUB_BYTES": output})
        if mixed_paths[0].read_bytes() != mixed_paths[1].read_bytes():
            raise v.VerifyError("same-repository mixed GitHub bytes differ from main-built expectations")
        result["mixed_github"] = {"identical": True, "sha256": v.sha(mixed_paths[1]), "golden_asserted_in_tree": True, "golden_baseline": result["baseline"]}
        paths = []
        for label, tree in (("baseline", args.baseline), ("candidate", root)):
            work = out / label
            work.mkdir(exist_ok=True)
            source = work / "contracts_test.go"
            source.write_text(v.DIFF_GO)
            overlay = work / "overlay.json"
            overlay.write_text(json.dumps({"Replace": {str(tree / v.TARGET): str(source)}}))
            path = work / "contracts.json"
            paths.append(path)
            print(label + " depth contracts", flush=True)
            v.invoke(["go", "test", "-overlay", str(overlay), "./internal/queue", "-run", "^TestADR00081Differential$", "-count=1"], tree, out, label + "-contracts", env_extra={"ADR_DIFF": path})
        result["contracts"] = v.compare_contracts(*paths)
        if not result["contracts"]["identical"]:
            raise v.VerifyError("depth-1/4 contracts differ")
        result["contracts"] = {"identical": True, "records": len(json.loads(paths[1].read_text())), "sha256": v.sha(paths[1])}
        print("strict live-journal replay", flush=True)
        v.invoke([sys.executable, str(root / "scripts/check-live-journal-replay.py"), "--baseline", str(args.baseline), "--candidate", str(root), "--events", str(v.EVENTS), "--queues", str(v.QUEUES), "--strict-state", "--out", str(out / "replay.json")], root, out, "strict-replay", mem="6GiB")
        replay = json.loads((out / "replay.json").read_text())
        result["replay"] = {key: replay[key] for key in ("verdict", "state_mode", "actions_identical", "structural_state_identical", "snapshot_hashes_identical", "boundary_checkpoints_identical", "boundary_probe_count", "non_allowlisted_fields", "gate2", "gate4", "self_test", "self_test_exit_status")}
        result["replay"]["full_report_sha256"] = v.sha(out / "replay.json")
        if result["replay"]["verdict"] != "PASS":
            raise v.VerifyError("strict replay did not pass")
        print("legacy raw Git objects, refs and command bytes", flush=True)
        # This driver acquires the heavy lock around each Go child itself.
        legacy = subprocess.run([sys.executable, str(root / "scripts/check-adr0008-3a-legacy.py"), "--baseline", str(args.baseline), "--candidate", str(root), "--out", str(out / "legacy")], cwd=root, capture_output=True, text=True)
        (out / "legacy-stdout.log").write_text(legacy.stdout)
        (out / "legacy-stderr.log").write_text(legacy.stderr)
        if legacy.returncode:
            raise v.VerifyError("legacy Git probe failed; see legacy logs")
        result["legacy"] = json.loads((out / "legacy/report.json").read_text())
        print("seeded simulator in both trees", flush=True)
        result["simulator"] = v.compare_simulator(args.baseline, root, out)
        result["stress"] = run_stress(v, root, out)
        binary = root / "bin/openmerge"
        result["binary"] = {"path": str(binary), "sha256": v.sha(binary)}
        result["verdict"] = "PASS"
    except Exception as error:
        result["verdict"] = "FAIL"
        result["error"] = str(error)
    finally:
        if original_database is None:
            os.environ.pop("OPENMERGE_TEST_DATABASE_URL", None)
        else:
            os.environ["OPENMERGE_TEST_DATABASE_URL"] = original_database
        if created:
            scratch_sql(maintenance, f'DROP DATABASE "{database_name}"')
    (out / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
    manifest = {str(path.relative_to(out)): v.sha(path) for path in sorted(out.rglob("*")) if path.is_file() and path.name != "manifest.json"}
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({key: result[key] for key in ("verdict", "error", "candidate") if key in result}), flush=True)
    return result["verdict"] != "PASS"


if __name__ == "__main__":
    sys.exit(main())
