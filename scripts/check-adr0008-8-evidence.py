#!/usr/bin/env python3
"""Offline PR 8 equivalence: strict replay, contracts, simulator, reporting bytes.

Requires clean baseline/candidate checkouts and the existing exported journal.
Never connects to a database, forge, or live status API. Each Go process inherits
or directly uses the shared host-load lock and limits. Reports retain commands,
exits, fixture hashes, and raw logs under --out.
"""
import argparse
import ast
import hashlib
import importlib.util
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--events', type=Path, required=True)
    parser.add_argument('--queues', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    baseline, out = args.baseline.resolve(), args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    spec = importlib.util.spec_from_file_location('adr_verify', ROOT / 'scripts/adr-0008-1-verify.py')
    verify = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(verify)
    report = {'commands': verify.COMMANDS, 'offline_only': True, 'trial_class': 'Hold'}
    try:
        report['baseline_commit'] = verify.check_tree('baseline', baseline)
        report['candidate_commit'] = verify.check_tree('candidate', ROOT)
        report['exports'] = verify.exports(args.events, args.queues)
        report['wire_fixtures'] = verify.compare_fixtures(baseline, ROOT)
        contract_paths = []
        for label, tree in [('baseline', baseline), ('candidate', ROOT)]:
            work = out / label
            work.mkdir(exist_ok=True)
            source = work / 'contracts_test.go'
            source.write_text(verify.DIFF_GO)
            overlay = work / 'contracts-overlay.json'
            overlay.write_text(json.dumps({'Replace': {str(tree / verify.TARGET): str(source)}}))
            result = work / 'contracts.json'
            verify.invoke(['go', 'test', '-overlay', str(overlay), './internal/queue', '-run',
                           '^TestADR00081Differential$', '-count=1'], tree, out,
                          label + '-contracts', env_extra={'ADR_DIFF': result})
            contract_paths.append(result)
        report['contracts'] = verify.compare_contracts(*contract_paths)
        if not report['contracts']['identical']:
            raise verify.VerifyError('contract checkpoints differ')
        # The previous decoder rejects the new event. The journal-only evidence
        # changes no snapshot shape; legacy v6 is covered by replay/tests.
        old_source = out / 'baseline' / 'compatibility_test.go'
        old_source.write_text('''package queue
import("os";"testing")
func TestPR8OldDecoder(t *testing.T){raw,err:=os.ReadFile(os.Getenv("CHECKS_FIXTURE"));if err!=nil{t.Fatal(err)};if _,err=DecodeEvent(raw);err==nil{t.Fatal("old decoder accepted landing checks")}}
''')
        overlay = out / 'baseline' / 'compatibility-overlay.json'
        overlay.write_text(json.dumps({'Replace': {str(baseline / verify.TARGET): str(old_source)}}))
        verify.invoke(['go', 'test', '-overlay', str(overlay), './internal/queue', '-run',
                       '^TestPR8OldDecoder$', '-count=1'], baseline, out, 'old-decoder',
                      env_extra={'CHECKS_FIXTURE': ROOT / 'internal/queue/testdata/wire/v1/event_landing_checks_recorded.json'})
        report['old_decoder'] = 'rejects landing_checks_recorded; drain/backup required before authority emission'
        replay_path = out / 'replay.json'
        verify.invoke([sys.executable, str(ROOT / 'scripts/check-live-journal-replay.py'),
                       '--baseline', str(baseline), '--candidate', str(ROOT),
                       '--events', str(args.events.resolve()), '--queues', str(args.queues.resolve()),
                       '--strict-state', '--out', str(replay_path)], ROOT, out, 'strict-replay', mem='6GiB')
        report['replay'] = json.loads(replay_path.read_text())
        if report['replay']['verdict'] != 'PASS':
            raise verify.VerifyError('strict replay failed')
        # Compare complete serialized bytes of the existing reporting fixtures,
        # with no additive-field allowance and the identical probe in both.
        parsed = ast.parse((ROOT / 'scripts/check-legacy-reporting.py').read_text())
        probe = next(ast.literal_eval(node.value) for node in parsed.body
                     if isinstance(node, ast.Assign) and any(isinstance(x, ast.Name) and x.id == 'PROBE' for x in node.targets))
        probe = probe.replace('// CANDIDATE_IMPORT', '').replace('// CANDIDATE_SUFFIX', '')
        traces = []
        for label, tree in [('baseline', baseline), ('candidate', ROOT)]:
            source = out / label / 'reporting_test.go'
            source.write_text(probe)
            overlay = out / label / 'reporting-overlay.json'
            overlay.write_text(json.dumps({'Replace': {str(tree / 'internal/status/legacy_reporting_probe_test.go'): str(source)}}))
            result = verify.invoke(['go', 'test', '-overlay', str(overlay), './internal/status', '-run',
                                    '^TestLegacyReportingProbe$', '-count=1', '-v'], tree, out, label + '-reporting')
            traces.append(sorted(line for line in result.stdout.splitlines() if line.startswith('LEGACY-REPORTING ')))
        if len(traces[0]) != 6 or traces[0] != traces[1]:
            raise verify.VerifyError('legacy reporting bytes differ')
        report['legacy_reporting'] = {'fixtures': 6, 'complete_status_and_check_bytes_identical': True,
                                      'sha256': hashlib.sha256(('\n'.join(traces[0]) + '\n').encode()).hexdigest()}
        report['simulator'] = verify.compare_simulator(baseline, ROOT, out)
        report['verdict'] = 'PASS'
    except (verify.VerifyError, OSError, ValueError) as err:
        report.update(verdict='FAIL', error=str(err))
    (out / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({k: v for k, v in report.items() if k != 'replay'}, indent=2))
    return 0 if report['verdict'] == 'PASS' else 1


if __name__ == '__main__':
    sys.exit(main())
