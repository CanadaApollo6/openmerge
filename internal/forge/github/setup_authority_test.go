package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/config"
)

const setupCandidate = "version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/owners}\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"

func authorityOptions(t *testing.T) RulesOptions {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "proposed.yml")
	must(t, os.WriteFile(path, []byte(setupCandidate), 0600))
	return RulesOptions{Repo: "acme/project", AppID: 42, Authority: "openmerge", Config: path, Backup: filepath.Join(dir, "backup.json"), Apply: true}
}

func runAuthority(t *testing.T, f *apiFixture, opts RulesOptions) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := setupRules(t.Context(), opts, "setup-admin-secret", &out, f.server.URL+"/api/v3/")
	return out.String(), err
}

func managedObject(t *testing.T, f *apiFixture, name string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, r := range f.setupRules {
		if r["name"] == name {
			if found != nil {
				t.Fatal("duplicate managed rule", name)
			}
			found = r
		}
	}
	if found == nil {
		t.Fatal("missing managed rule", name)
	}
	return found
}

func TestAuthorityInstallRestoreAndLegacySuccessor(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	// Unknown retained fields, rules and parameters survive rename AND restore.
	fence := f.setupRules[11]
	fence["future_setting"] = map[string]any{"keep": true}
	rules := fence["rules"].([]any)
	rules[1].(map[string]any)["future_rule_field"] = "retained"
	rules[1].(map[string]any)["parameters"].(map[string]any)["future_parameter"] = "retained"
	before, _ := json.Marshal(f.setupRules)
	opts.Apply = false
	out, err := runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != 0 || !strings.Contains(out, "Preview only") || !strings.Contains(out, "refs/heads/openmerge/verify/**/*") {
		t.Fatalf("preview: %s %v", out, err)
	}
	if _, err := os.Stat(opts.Backup); !os.IsNotExist(err) {
		t.Fatal("preview wrote backup")
	}
	opts.Apply = true
	_, err = runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != 2 {
		t.Fatal("writes", f.setupWrites)
	}
	base := managedObject(t, f, "openmerge-base")
	refs := managedObject(t, f, "openmerge-refs")
	if fmt.Sprint(refs["id"]) != "11" || refs["future_setting"].(map[string]any)["keep"] != true {
		t.Fatal("rename lost ID or unknown setting")
	}
	var parsed Ruleset
	data, _ := json.Marshal(base)
	must(t, json.Unmarshal(data, &parsed))
	if !hasFenceRules(parsed) || len(parsed.BypassActors) != 2 {
		t.Fatal("unsafe base", base)
	}
	data, _ = json.Marshal(refs)
	must(t, json.Unmarshal(data, &parsed))
	if !managedRefsShape(parsed, 42, "main") {
		t.Fatal("unsafe refs", refs)
	}
	info, err := os.Stat(opts.Backup)
	must(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe backup mode")
	}
	saved, err := readRulesBackup(opts.Backup)
	must(t, err)
	if saved.Version != 1 || len(saved.Edits) != 2 || saved.Edits[0].ID != 0 || saved.Edits[1].ID != 11 {
		t.Fatalf("capture %+v", saved)
	}
	raw, err := os.ReadFile(opts.Backup)
	must(t, err)
	if bytes.Contains(raw, []byte("setup-admin-secret")) || bytes.Contains(raw, []byte("main-protection")) {
		t.Fatal("backup contains unrelated settings or credentials")
	}
	for _, apply := range []bool{false, true} {
		out, err := runRules(t, f, apply)
		must(t, err)
		if !strings.Contains(out, "No ruleset changes needed") || f.setupWrites != 2 {
			t.Fatal("legacy duplicated fence after rename", out)
		}
	}
	_, err = runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != 2 {
		t.Fatal("retry repeated writes")
	}
	// A fresh plan also converges to zero changes.
	fresh := opts
	fresh.Backup = filepath.Join(t.TempDir(), "fresh.json")
	out, err = runAuthority(t, f, fresh)
	must(t, err)
	if !strings.Contains(out, "No ruleset changes needed") || f.setupWrites != 2 {
		t.Fatal("fresh rerun", out)
	}
	restore := RulesOptions{Repo: opts.Repo, AppID: 42, Authority: "openmerge", Restore: opts.Backup}
	_, err = runAuthority(t, f, restore)
	must(t, err)
	if f.setupWrites != 2 {
		t.Fatal("restore preview mutated")
	}
	restore.Apply = true
	_, err = runAuthority(t, f, restore)
	must(t, err)
	after, _ := json.Marshal(f.setupRules)
	if !bytes.Equal(before, after) {
		t.Fatalf("restore lost settings\n%s\n%s", before, after)
	}
	_, err = runAuthority(t, f, restore)
	must(t, err)
	if f.setupWrites != 4 {
		t.Fatal("restore retry repeated writes")
	}
	for _, apply := range []bool{false, true} {
		out, err := runRules(t, f, apply)
		must(t, err)
		if !strings.Contains(out, "No ruleset changes needed") || f.setupWrites != 4 {
			t.Fatal("legacy after rollback", out)
		}
	}
}

func TestAuthoritySelectorUnionAndSingleUpdateRotation(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	expanded := strings.Replace(setupCandidate, "queues:", "queues:", 1) + "- name: release\n  base: release/**\n  merge_when: [check-success = ci]\n"
	must(t, os.WriteFile(opts.Config, []byte(expanded), 0600))
	opts.Backup = filepath.Join(t.TempDir(), "expand.json")
	_, err = runAuthority(t, f, opts)
	must(t, err)
	base := managedObject(t, f, "openmerge-base")
	data, _ := json.Marshal(base["conditions"])
	if !bytes.Contains(data, []byte("refs/heads/release/**/*")) {
		t.Fatal("missing recursive selector", string(data))
	}
	// A proposed removal cannot prune already installed obligations.
	must(t, os.WriteFile(opts.Config, []byte(setupCandidate), 0600))
	opts.Backup = filepath.Join(t.TempDir(), "remove.json")
	_, err = runAuthority(t, f, opts)
	must(t, err)
	data, _ = json.Marshal(base["conditions"])
	if !bytes.Contains(data, []byte("refs/heads/release/**/*")) {
		t.Fatal("selector union pruned coverage")
	}
	rotated := strings.Replace(expanded, "team: acme/owners", "team: acme/emergency", 1)
	must(t, os.WriteFile(opts.Config, []byte(rotated), 0600))
	opts.Backup = filepath.Join(t.TempDir(), "rotate.json")
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/orgs/acme/teams/emergency") {
			f.json(w, map[string]any{"id": 99, "slug": "emergency", "organization": map[string]any{"id": 55, "login": "acme"}})
			return true
		}
		return false
	}
	writes := f.setupWrites
	_, err = runAuthority(t, f, opts)
	if err == nil || !strings.Contains(err.Error(), "--rotate-break-glass") || f.setupWrites != writes {
		t.Fatal("implicit actor rotation", err)
	}
	opts.RotateBreakGlass = true
	var actorCounts []int
	original := f.override
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == http.MethodPut {
			raw, readErr := io.ReadAll(r.Body)
			must(t, readErr)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var body struct {
				Actors []BypassActor `json:"bypass_actors"`
			}
			must(t, json.Unmarshal(raw, &body))
			actorCounts = append(actorCounts, len(body.Actors))
		}
		return original(w, r)
	}
	_, err = runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != writes+1 || !reflect.DeepEqual(actorCounts, []int{2}) {
		t.Fatalf("rotation was not one exact-list write: %v", actorCounts)
	}
	saved, err := readRulesBackup(opts.Backup)
	must(t, err)
	if len(saved.Edits) != 1 || saved.Edits[0].Name != "openmerge-base" {
		t.Fatal("rotation touched refs")
	}
	restore := RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true}
	_, err = runAuthority(t, f, restore)
	must(t, err)
	data, _ = json.Marshal(base["bypass_actors"])
	if !bytes.Contains(data, []byte("Team")) || bytes.Contains(data, []byte("99")) {
		t.Fatal("rotation restore lost exact old actor", string(data))
	}
}

func TestAuthorityManagedCollisionsAndActorRejection(t *testing.T) {
	for _, kind := range []string{"duplicate fence", "ambiguous pair", "inherited name", "inherited refs", "unsafe successor", "unprotected fence", "excluded fence", "unknown conditions", "unknown ref field", "unknown actor field", "user actor", "mismatched actor", "wrong org", "unresolved team"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			clone := func() map[string]any {
				data, _ := json.Marshal(f.setupRules[11])
				var out map[string]any
				must(t, json.Unmarshal(data, &out))
				out["id"] = 12
				return out
			}
			switch kind {
			case "duplicate fence":
				f.setupRules[12] = clone()
			case "ambiguous pair":
				f.setupRules[12] = clone()
				f.setupRules[12]["name"] = "openmerge-refs"
			case "inherited name":
				f.setupRules[11]["source_type"] = "Organization"
			case "inherited refs":
				f.setupRules[11]["name"] = "openmerge-refs"
				f.setupRules[11]["source_type"] = "Organization"
			case "unsafe successor":
				f.setupRules[11]["name"] = "openmerge-refs"
				f.setupRules[11]["bypass_actors"] = []any{}
			case "unprotected fence":
				f.setupRules[11]["enforcement"] = "disabled"
			case "excluded fence":
				f.setupRules[11]["conditions"] = map[string]any{"ref_name": map[string]any{"include": fencePatterns, "exclude": []string{fencePrefix + "main"}}}
			case "unknown conditions":
				f.setupRules[11]["conditions"].(map[string]any)["future_narrowing"] = true
			case "unknown ref field":
				ref := f.setupRules[11]["conditions"].(map[string]any)["ref_name"].(map[string]any)
				delete(ref, "exclude")
				ref["future_narrowing"] = true
			case "unknown actor field":
				f.setupRules[11]["bypass_actors"].([]any)[0].(map[string]any)["future_bypass"] = true
			case "user actor":
				opts.BreakGlass = `{"user":"alice"}`
			case "mismatched actor":
				opts.BreakGlass = `{"team":"acme/emergency"}`
			case "wrong org":
				must(t, os.WriteFile(opts.Config, []byte(strings.Replace(setupCandidate, "team: acme/owners", "team: other/emergency", 1)), 0600))
			case "unresolved team":
				must(t, os.WriteFile(opts.Config, []byte(strings.Replace(setupCandidate, "team: acme/owners", "team: acme/emergency", 1)), 0600))
				f.override = func(w http.ResponseWriter, r *http.Request) bool {
					if strings.Contains(r.URL.Path, "/teams/") {
						f.json(w, map[string]any{"id": 0})
						return true
					}
					return false
				}
			}
			_, err := runAuthority(t, f, opts)
			if err == nil || f.setupWrites != 0 {
				t.Fatalf("unsafe mutation: %v writes=%d", err, f.setupWrites)
			}
		})
	}
}

func TestAuthorityRetainsUnrelatedAndInheritedContentBypasses(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		f := rulesAPI(t)
		opts := authorityOptions(t)
		if inherited {
			f.setupRules[10]["source_type"], f.setupRules[10]["source"] = "Organization", "acme"
		}
		// Even compatible but unbypassed content is left untouched by setup.
		f.setupRules[10]["bypass_actors"] = []any{}
		before, _ := json.Marshal(f.setupRules[10])
		_, err := runAuthority(t, f, opts)
		must(t, err)
		after, _ := json.Marshal(f.setupRules[10])
		if !bytes.Equal(before, after) {
			t.Fatal("edited retained policy")
		}
	}
	f := rulesAPI(t)
	opts := authorityOptions(t)
	before, _ := json.Marshal(f.setupRules[10])
	_, err := runAuthority(t, f, opts)
	must(t, err)
	after, _ := json.Marshal(f.setupRules[10])
	if !bytes.Equal(before, after) {
		t.Fatal("removed preactivation App bypass")
	}
}

func TestAuthorityFaultEveryHTTPBoundary(t *testing.T) {
	// Record the whole transaction, then fail every individual read/write in a
	// fresh fixture. Every successful prefix is resumed with the immutable plan.
	f := rulesAPI(t)
	opts := authorityOptions(t)
	var trace []string
	f.override = func(_ http.ResponseWriter, r *http.Request) bool {
		trace = append(trace, r.Method+" "+r.URL.RequestURI())
		return false
	}
	_, err := runAuthority(t, f, opts)
	must(t, err)
	for index, step := range trace {
		t.Run(fmt.Sprintf("%02d_%s", index, step), func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			count := 0
			f.override = func(w http.ResponseWriter, _ *http.Request) bool {
				current := count
				count++
				if current == index {
					f.apiError(w, 503, "injected setup-admin-secret")
					return true
				}
				return false
			}
			out, err := runAuthority(t, f, opts)
			if err == nil || strings.Contains(err.Error(), "setup-admin-secret") {
				t.Fatalf("failure not propagated safely: %s %v", out, err)
			}
			f.override = nil
			_, err = runAuthority(t, f, opts)
			must(t, err)
			if f.setupWrites != 2 {
				t.Fatalf("retry duplicated writes after %s: %d", step, f.setupWrites)
			}
			restore := RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true}
			_, err = runAuthority(t, f, restore)
			must(t, err)
			if len(f.setupRules) != 2 || f.setupRules[11]["name"] != "openmerge-fence" {
				t.Fatal("restore after partial failure")
			}
		})
	}
}

func TestAuthorityConcurrentEditsAndBackupGuards(t *testing.T) {
	for _, kind := range []string{"before first", "before second", "after write", "restore intervening", "restore replacement", "restore name collision", "backup exists", "backup token", "backup unavailable", "missing backup", "bad version", "wrong binding", "output failure"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			switch kind {
			case "before first", "before second", "after write":
				repos := 0
				f.override = func(_ http.ResponseWriter, r *http.Request) bool {
					if r.URL.Path == "/api/v3/repos/acme/project" {
						repos++
						if kind == "before first" && repos == 3 || kind == "before second" && repos == 5 || kind == "after write" && repos == 4 {
							f.setupRules[10]["name"] = "concurrent-edit"
						}
					}
					return false
				}
			case "restore intervening", "restore replacement", "restore name collision":
				_, err := runAuthority(t, f, opts)
				must(t, err)
				switch kind {
				case "restore intervening":
					managedObject(t, f, "openmerge-base")["enforcement"] = "disabled"
				case "restore replacement":
					fence := f.setupRules[11]
					delete(f.setupRules, 11)
					fence["id"] = 12
					f.setupRules[12] = fence
				case "restore name collision":
					data, _ := json.Marshal(f.setupRules[11])
					var duplicate map[string]any
					must(t, json.Unmarshal(data, &duplicate))
					duplicate["id"], duplicate["name"] = 12, "openmerge-fence"
					f.setupRules[12] = duplicate
				}
				opts = RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true}
			case "backup exists":
				must(t, os.WriteFile(opts.Backup, []byte("keep me"), 0600))
			case "backup token":
				f.setupRules[11]["credential"] = "setup-admin-secret"
			case "backup unavailable":
				opts.Backup = filepath.Join(opts.Backup, "missing", "capture.json")
			case "missing backup":
				opts.Backup = ""
			case "bad version":
				must(t, os.WriteFile(opts.Backup, []byte(`{"version":2}`), 0600))
			case "wrong binding":
				_, err := runAuthority(t, f, opts)
				must(t, err)
				must(t, os.WriteFile(opts.Config, []byte(setupCandidate+"# other reviewed plan\n"), 0600))
			}
			writes := f.setupWrites
			var err error
			if kind == "output failure" {
				err = setupRules(t.Context(), opts, "setup-admin-secret", failingOutput{}, f.server.URL+"/api/v3/")
			} else {
				_, err = runAuthority(t, f, opts)
			}
			if err == nil {
				t.Fatal("unsafe operation succeeded")
			}
			expected := writes
			if kind == "before second" || kind == "after write" {
				expected++
			}
			if f.setupWrites != expected {
				t.Fatalf("writes=%d want=%d: %v", f.setupWrites, expected, err)
			}
			if kind == "backup token" {
				if _, err := os.Stat(opts.Backup); !os.IsNotExist(err) {
					t.Fatal("credential persisted")
				}
			}
		})
	}
}

// Abrupt process exit leaves only the pre-write capture, never an in-memory
// progress object. The parent owns the httptest server and observes the write.
func TestAuthorityCrashAfterFirstRuleset(t *testing.T) {
	if api := os.Getenv("OPENMERGE_SETUP_CRASH_API"); api != "" {
		opts := RulesOptions{Repo: "acme/project", AppID: 42, Authority: "openmerge", Config: os.Getenv("OPENMERGE_SETUP_CRASH_CONFIG"), Backup: os.Getenv("OPENMERGE_SETUP_CRASH_BACKUP"), Apply: true}
		_ = setupRules(context.Background(), opts, "setup-admin-secret", crashSetupOutput{}, api)
		os.Exit(74)
	}
	f := rulesAPI(t)
	opts := authorityOptions(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestAuthorityCrashAfterFirstRuleset$")
	cmd.Env = append(os.Environ(), "OPENMERGE_SETUP_CRASH_API="+f.server.URL+"/api/v3/", "OPENMERGE_SETUP_CRASH_CONFIG="+opts.Config, "OPENMERGE_SETUP_CRASH_BACKUP="+opts.Backup)
	output, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 73 {
		t.Fatalf("crash helper: %v %s", err, output)
	}
	f.mu.Lock()
	writes := f.setupWrites
	f.mu.Unlock()
	if writes != 1 {
		t.Fatalf("crash happened after %d writes", writes)
	}
	_, err = runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != 2 {
		t.Fatal("crash retry duplicated ruleset")
	}
}

type crashSetupOutput struct{}

func (crashSetupOutput) Write(data []byte) (int, error) {
	if bytes.HasPrefix(data, []byte("Completed ")) {
		os.Exit(73)
	}
	return len(data), nil
}

func TestLegacySetupGoldenUnchanged(t *testing.T) {
	f := rulesAPI(t)
	for _, apply := range []bool{false, true} {
		out, err := runRules(t, f, apply)
		must(t, err)
		want := "Repository acme/project; base main; App 42\nNo ruleset changes needed.\nRulesets allow base landing and queue pushes; fence ownership is exclusive to the App.\n"
		if out != want {
			t.Fatalf("legacy output changed: %q", out)
		}
	}
}

func TestProtectedRefCompilerExactFamilies(t *testing.T) {
	coverage := authority.CompileProtectedRefCoverage()
	if len(coverage.Includes) != 8 {
		t.Fatal(coverage)
	}
	for _, family := range []string{"fence", "queue", "green", "verify"} {
		for _, tail := range []string{"main", "release/v1/batch"} {
			ok, err := coverage.ContainsRef("refs/heads/openmerge/"+family+"/"+tail, "refs/heads/main")
			must(t, err)
			if !ok {
				t.Fatal("missing coverage", family, tail)
			}
		}
	}
	for _, ref := range []string{"refs/heads/main", "refs/heads/openmerge/submission/123"} {
		ok, err := coverage.ContainsRef(ref, "refs/heads/main")
		must(t, err)
		if ok {
			t.Fatal("unintended ref", ref)
		}
	}
}

func TestAuthorityCandidateAndLegacySuccessorPurePlans(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	client, err := setupClient(f.server.URL+"/api/v3/", "setup-admin-secret")
	must(t, err)
	snap, err := client.rules(t.Context(), opts.Repo)
	must(t, err)
	before, _ := json.Marshal(snap)
	candidate, _, err := config.LoadAuthorityCandidate([]byte(setupCandidate))
	must(t, err)
	plan, err := planAuthorityRules(snap, opts, candidate, BypassActor{ActorID: new(int64(98)), ActorType: "Team", BypassMode: "always"})
	must(t, err)
	after, _ := json.Marshal(snap)
	if len(plan.Edits) != 0 || !bytes.Equal(before, after) {
		t.Fatal("non-pure or non-idempotent plan")
	}
	refs := managedObject(t, f, "openmerge-refs")
	data, _ := json.Marshal(refs)
	var rs Ruleset
	must(t, json.Unmarshal(data, &rs))
	rs.BypassActors = append(rs.BypassActors, BypassActor{ActorID: new(int64(5)), ActorType: "RepositoryRole", BypassMode: "always"})
	if managedRefsShape(rs, 42, "main") {
		t.Fatal("recognized extra bypass")
	}
	if !slices.Contains(authority.CompileProtectedRefCoverage().Includes, fencePrefix+"**/*") {
		t.Fatal("lost fence")
	}
}

func TestAuthorityRestoreFaultEveryHTTPBoundary(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	restore := RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true}
	var trace []string
	f.override = func(_ http.ResponseWriter, r *http.Request) bool {
		trace = append(trace, r.Method+" "+r.URL.RequestURI())
		return false
	}
	_, err = runAuthority(t, f, restore)
	must(t, err)
	for index, step := range trace {
		t.Run(fmt.Sprintf("%02d_%s", index, step), func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			_, err := runAuthority(t, f, opts)
			must(t, err)
			restore := RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true}
			count := 0
			f.override = func(w http.ResponseWriter, _ *http.Request) bool {
				current := count
				count++
				if current == index {
					f.apiError(w, 503, "injected")
					return true
				}
				return false
			}
			_, err = runAuthority(t, f, restore)
			if err == nil {
				t.Fatal("restore fault not returned")
			}
			f.override = nil
			_, err = runAuthority(t, f, restore)
			must(t, err)
			if f.setupWrites != 4 || len(f.setupRules) != 2 || f.setupRules[11]["name"] != "openmerge-fence" {
				t.Fatal("restore retry lost exact prior state")
			}
		})
	}
}

func TestAuthorityUncertainWritesAndActorAPIRejection(t *testing.T) {
	for _, kind := range []string{"create response lost", "rename response lost", "actor rejected"} {
		t.Run(kind, func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPost && r.Method != http.MethodPut {
					return false
				}
				if kind == "actor rejected" {
					f.apiError(w, 422, "bypass_actors: Integration rejected setup-admin-secret")
					return true
				}
				if kind == "rename response lost" && r.Method != http.MethodPut {
					return false
				}
				var body map[string]any
				must(t, json.NewDecoder(r.Body).Decode(&body))
				id := int64(11)
				if r.Method == http.MethodPost {
					id = f.id()
					f.setupRules[id] = map[string]any{"id": id, "source_type": "Repository", "source": "acme/project"}
				}
				for key, value := range body {
					f.setupRules[id][key] = value
				}
				f.setupWrites++
				f.apiError(w, 503, "response lost after committed write")
				return true
			}
			out, err := runAuthority(t, f, opts)
			if err == nil || !strings.Contains(out, "pending or uncertain") || strings.Contains(err.Error(), "setup-admin-secret") {
				t.Fatalf("unsafe partial failure: %s %v", out, err)
			}
			f.override = nil
			_, err = runAuthority(t, f, opts)
			must(t, err)
			if f.setupWrites != 2 {
				t.Fatal("uncertain-write retry duplicated rule", f.setupWrites)
			}
		})
	}
}

func TestLegacySuccessorKeepsExtraCoverageAndRejectsAmbiguity(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	refs := managedObject(t, f, "openmerge-refs")
	conditions := refs["conditions"].(map[string]any)["ref_name"].(map[string]any)
	conditions["include"] = append(conditions["include"].([]any), "refs/heads/openmerge/green/retained-obligation/**/*")
	before, _ := json.Marshal(refs)
	for _, apply := range []bool{false, true} {
		_, err := runRules(t, f, apply)
		must(t, err)
	}
	after, _ := json.Marshal(refs)
	if !bytes.Equal(before, after) || f.setupWrites != 2 {
		t.Fatal("legacy successor changed extra coverage")
	}
	duplicate := map[string]any{}
	must(t, json.Unmarshal(before, &duplicate))
	duplicate["id"] = 12
	duplicate["name"] = "openmerge-fence"
	f.setupRules[12] = duplicate
	for _, apply := range []bool{false, true} {
		_, err := runRules(t, f, apply)
		if err == nil || f.setupWrites != 2 {
			t.Fatal("ambiguous pair accepted", err)
		}
	}
}

// These files were captured by running the unmodified legacy setup on 77b703e.
func TestLegacySetupMainGoldenByteIdentical(t *testing.T) {
	for _, variant := range []string{"default", "repair-preview", "repair-apply"} {
		t.Run(variant, func(t *testing.T) {
			f := rulesAPI(t)
			if variant != "default" {
				delete(f.setupRules, 11)
				f.setupRules[10]["bypass_actors"] = []any{}
			}
			out, err := runRules(t, f, variant == "repair-apply")
			if variant == "repair-preview" {
				if err == nil {
					t.Fatal("expected preview blockers")
				}
			} else {
				must(t, err)
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "setup-rules", "github-"+variant+".golden"))
			must(t, err)
			if !bytes.Equal(golden, []byte(out)) {
				t.Fatalf("legacy output changed from main:\n%s\nwant:\n%s", out, golden)
			}
		})
	}
}

func TestAuthorityIdempotenceWithAPIOrderingAndDefaults(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	// Live APIs may serialize object keys, actor/rule lists, and default-false
	// parameters differently from the request. None is an operator policy edit.
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/rulesets/") {
			return false
		}
		for _, object := range f.setupRules {
			if fmt.Sprint(object["id"]) != filepath.Base(r.URL.Path) {
				continue
			}
			raw, _ := json.Marshal(object)
			var fields map[string]json.RawMessage
			must(t, json.Unmarshal(raw, &fields))
			var actors []map[string]any
			must(t, json.Unmarshal(fields["bypass_actors"], &actors))
			slices.Reverse(actors)
			var items []string
			for _, actor := range actors {
				items = append(items, fmt.Sprintf(`{"bypass_mode":%q,"actor_type":%q,"actor_id":%v}`, actor["bypass_mode"], actor["actor_type"], actor["actor_id"]))
			}
			fields["bypass_actors"] = json.RawMessage("[" + strings.Join(items, ",") + "]")
			var rules []json.RawMessage
			must(t, json.Unmarshal(fields["rules"], &rules))
			slices.Reverse(rules)
			fields["rules"], _ = json.Marshal(rules)
			f.json(w, fields)
			return true
		}
		return false
	}
	_, err = runAuthority(t, f, opts)
	must(t, err)
	if f.setupWrites != 2 {
		t.Fatal("API representation differences caused writes")
	}
}

func TestAuthorityRestoreRefusesUnknownLargeNumericEdit(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	f.setupRules[11]["future_limit"] = json.Number("9007199254740992")
	_, err := runAuthority(t, f, opts)
	must(t, err)
	f.setupRules[11]["future_limit"] = json.Number("9007199254740993")
	writes := f.setupWrites
	_, err = runAuthority(t, f, RulesOptions{Repo: opts.Repo, AppID: 42, Restore: opts.Backup, Apply: true})
	if err == nil || f.setupWrites != writes {
		t.Fatal("restore overwrote an exact unknown numeric edit", err)
	}
}

func TestLegacySuccessorRejectsCoverageOutsideProtectedFamilies(t *testing.T) {
	for _, extra := range []string{"~ALL", "~DEFAULT_BRANCH", "refs/heads/main", "refs/heads/retained-obligation/**/*", "refs/heads/openmerge/*/*", "refs/heads/openmerge/gr*/*", "refs/heads/openmerge/submission/**/*"} {
		t.Run(extra, func(t *testing.T) {
			f := rulesAPI(t)
			opts := authorityOptions(t)
			_, err := runAuthority(t, f, opts)
			must(t, err)
			refs := managedObject(t, f, "openmerge-refs")
			conditions := refs["conditions"].(map[string]any)["ref_name"].(map[string]any)
			conditions["include"] = append(conditions["include"].([]any), extra)
			before, _ := json.Marshal(f.setupRules)
			for _, apply := range []bool{false, true} {
				_, err := runRules(t, f, apply)
				if err == nil || !strings.Contains(err.Error(), "incompatible successor shape") || f.setupWrites != 2 {
					t.Fatal("accepted unbounded successor", err)
				}
			}
			after, _ := json.Marshal(f.setupRules)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected successor mutated")
			}
		})
	}
}

func TestRulesBackupReuseRepairsPermissions(t *testing.T) {
	f := rulesAPI(t)
	opts := authorityOptions(t)
	_, err := runAuthority(t, f, opts)
	must(t, err)
	backup, err := readRulesBackup(opts.Backup)
	must(t, err)
	before, err := os.ReadFile(opts.Backup)
	must(t, err)
	for _, mode := range []os.FileMode{0644, 0666, 0700, 0600} {
		must(t, os.Chmod(opts.Backup, mode))
		must(t, writeRulesBackup(opts.Backup, backup))
		info, err := os.Stat(opts.Backup)
		must(t, err)
		after, err := os.ReadFile(opts.Backup)
		must(t, err)
		if info.Mode().Perm() != 0600 || !bytes.Equal(before, after) {
			t.Fatal("reuse changed capture or retained unsafe mode", info.Mode())
		}
	}
}
