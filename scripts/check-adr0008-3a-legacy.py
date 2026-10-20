#!/usr/bin/env python3
"""Compare legacy Git objects/refs against main using identical offline probes.

Only local bare remotes are used. Each Go invocation holds the shared host lock.
Reflection supplies a zero trailing PushOptions only where the candidate API
requires it, so the actual workload is identical across both revisions.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess

COMMON = r'''
func probeMust(t *testing.T,e error){t.Helper();if e!=nil{t.Fatal(e)}}
func probeCall(t *testing.T,g any,name string,args ...any)string{
 t.Helper();m:=reflect.ValueOf(g).MethodByName(name);vs:=[]reflect.Value{}
 for _,a:=range args{vs=append(vs,reflect.ValueOf(a))}
 for len(vs)<m.Type().NumIn(){vs=append(vs,reflect.Zero(m.Type().In(len(vs))))}
 out:=m.Call(vs);if e:=out[len(out)-1];!e.IsNil(){t.Fatal(e.Interface().(error))}
 if len(out)>1{return out[0].String()};return ""
}
'''
REAL = r'''package gitops
import("bytes";"context";"crypto/rand";"encoding/json";"os";"os/exec";"path/filepath";"reflect";"strings";"testing";"github.com/CanadaApollo6/openmerge/internal/forge")
''' + COMMON + r'''
func TestADR3aLegacyProbe(t *testing.T){
 original:=rand.Reader;defer func(){rand.Reader=original}();rows:=[]any{};ctx:=context.Background()
 for _,method:=range []forge.MergeMethod{forge.Merge,forge.Squash,forge.Rebase,forge.FastForward}{
  rand.Reader=bytes.NewReader(bytes.Repeat([]byte{0x5a},65536));f:=newFixture(t)
  trace:=filepath.Join(t.TempDir(),"commands");wrapper:=filepath.Join(t.TempDir(),"git")
  writeExecutable(t,wrapper,"printf 'CMD\\n' >> "+shellQuote(trace)+"\n"+"printf '%s\\n' \"$@\" >> "+shellQuote(trace)+"\nexec "+shellQuote(f.g.git)+" \"$@\"\n");f.g.git=wrapper
  save:=func(step string){cmd:=exec.Command("git","--git-dir="+f.remote,"cat-file","--batch-all-objects","--batch");cmd.Env=gitEnv();objects,e:=cmd.Output();probeMust(t,e);commands,e:=os.ReadFile(trace);probeMust(t,e);normalized:=strings.ReplaceAll(strings.ReplaceAll(string(commands),f.g.cfg.WorkDir,"<cache>"),filepath.Dir(f.remote),"<fixture>");rows=append(rows,map[string]any{"method":method,"step":step,"refs":gitCommand(t,f.remote,"",nil,"show-ref"),"objects":objects,"commands":normalized})}
  fence:=probeCall(t,f.g,"EnsureFence",ctx,testRepo,testFence);save("fence-create")
  prs:=f.prs[:2];if method==forge.FastForward{prs=f.prs[:1]}
  req:=forge.BuildRequest{Ref:"openmerge/queue/main/equiv",BaseSHA:f.base,PRs:prs,Method:method,BatchID:"equiv"}
  probeCall(t,f.g,"BuildTestRef",ctx,testRepo,req);save("build")
  req.BatchID="equiv-update";tested:=probeCall(t,f.g,"BuildTestRef",ctx,testRepo,req);save("build-update")
  fence=probeCall(t,f.g,"AdvanceFence",ctx,testRepo,testFence,fence,"abandon/equiv");save("fence-advance")
  land:=forge.LandRequest{Base:"main",ExpectedBaseSHA:f.base,TestedSHA:tested,Fence:testFence,ExpectedFence:fence,NextFenceID:"land/equiv",PRs:prs}
  if method==forge.FastForward{land.FastForwardHeadSHA=prs[len(prs)-1].HeadSHA}
  probeCall(t,f.g,"Land",ctx,testRepo,land);save("land")
  f.push("topic",prs[0].HeadSHA,false);probeCall(t,f.g,"DeleteBranch",ctx,testRepo,"topic",prs[0].HeadSHA);save("delete")
 }
 raw,e:=json.Marshal(rows);probeMust(t,e);probeMust(t,os.WriteFile(os.Getenv("LEGACY_OUTPUT"),raw,0600))
}
'''
FAKE = r'''package fake
import("context";"encoding/json";"os";"reflect";"testing";"github.com/CanadaApollo6/openmerge/internal/forge")
''' + COMMON + r'''
func TestADR3aLegacyProbe(t *testing.T){
 rows:=[]any{};ctx:=context.Background();repo:=forge.RepoRef{Path:"local/equivalence"};fenceRef:="openmerge/fence/main"
 for _,method:=range []forge.MergeMethod{forge.Merge,forge.Squash,forge.Rebase,forge.FastForward}{
  f:=NewDeterministic();base,_:=f.Init(repo,"main","fixture-fence");prs:=[]forge.BuildPR{}
  for _,n:=range []int{1,2}{pr,e:=f.OpenPR(repo,n,base,true);probeMust(t,e);prs=append(prs,forge.BuildPR{Number:n,HeadSHA:pr.HeadSHA})}
  save:=func(step string){r:=f.repo(repo);raw,e:=json.Marshal(map[string]any{"refs":r.refs,"objects":r.commits});probeMust(t,e);rows=append(rows,map[string]any{"method":method,"step":step,"snapshot":string(raw)})}
  fence:=probeCall(t,f,"EnsureFence",ctx,repo,fenceRef);save("fence-create")
  if method==forge.FastForward{prs=prs[:1]}
  req:=forge.BuildRequest{Ref:"openmerge/queue/main/equiv",BaseSHA:base,PRs:prs,Method:method,BatchID:"equiv"}
  probeCall(t,f,"BuildTestRef",ctx,repo,req);save("build")
  req.BatchID="equiv-update";tested:=probeCall(t,f,"BuildTestRef",ctx,repo,req);save("build-update")
  fence=probeCall(t,f,"AdvanceFence",ctx,repo,fenceRef,fence,"abandon/equiv");save("fence-advance")
  land:=forge.LandRequest{Base:"main",ExpectedBaseSHA:base,TestedSHA:tested,Fence:fenceRef,ExpectedFence:fence,NextFenceID:"land/equiv",PRs:prs}
  if method==forge.FastForward{land.FastForwardHeadSHA=prs[len(prs)-1].HeadSHA}
  probeCall(t,f,"Land",ctx,repo,land);save("land")
  probeMust(t,f.PushRef(repo,"topic",prs[0].HeadSHA));probeCall(t,f,"DeleteBranch",ctx,repo,"topic",prs[0].HeadSHA);save("delete")
 }
 raw,e:=json.Marshal(rows);probeMust(t,e);probeMust(t,os.WriteFile(os.Getenv("LEGACY_OUTPUT"),raw,0600))
}
'''


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--baseline', type=Path, required=True)
    parser.add_argument('--candidate', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    report = {'commands': [], 'comparisons': {}, 'offline_only': True}
    for label, root in [('baseline', args.baseline), ('candidate', args.candidate)]:
        root = root.resolve()
        report[label] = subprocess.check_output(['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True).strip()
        for driver, source, target in [('gitops', REAL, 'internal/gitops/first_parent_test.go'), ('fake', FAKE, 'internal/forge/fake/compact_test.go')]:
            work = args.out / f'{label}-{driver}'
            work.mkdir(exist_ok=True)
            probe, output = work / 'probe_test.go', work / 'objects.json'
            probe.write_text(source)
            overlay = work / 'overlay.json'
            overlay.write_text(json.dumps({'Replace': {str(root / target): str(probe.resolve())}}))
            cmd = ['flock', '/home/riels/.cache/openmerge-heavy.lock', 'env', 'GOFLAGS=-p=2', 'GOMEMLIMIT=4GiB', 'GOPROXY=off', 'GOSUMDB=off', 'GOTOOLCHAIN=local', f'LEGACY_OUTPUT={output.resolve()}', 'go', 'test', '-overlay', str(overlay.resolve()), f'./internal/{"forge/fake" if driver == "fake" else "gitops"}', '-run', '^TestADR3aLegacyProbe$', '-count=1']
            run = subprocess.run(cmd, cwd=root, capture_output=True, text=True)
            (work / 'stdout.log').write_text(run.stdout)
            (work / 'stderr.log').write_text(run.stderr)
            report['commands'].append({'command': cmd, 'cwd': str(root), 'exit': run.returncode})
            if run.returncode:
                (args.out / 'report.json').write_text(json.dumps(report, indent=2))
                raise SystemExit(f'{label} {driver} failed: {work}')
    for driver in ['gitops', 'fake']:
        base, candidate = [args.out / f'{label}-{driver}' / 'objects.json' for label in ['baseline', 'candidate']]
        same = base.read_bytes() == candidate.read_bytes()
        report['comparisons'][driver] = {'identical': same, 'snapshots': len(json.loads(base.read_text())), 'baseline_sha256': sha(base), 'candidate_sha256': sha(candidate)}
    (args.out / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    if not all(x['identical'] for x in report['comparisons'].values()):
        raise SystemExit('legacy object/ref divergence')
    print(json.dumps(report['comparisons'], indent=2))


if __name__ == '__main__':
    main()
