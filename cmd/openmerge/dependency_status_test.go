package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/dependency/dependencytest/statusfixture"
	"github.com/CanadaApollo6/openmerge/internal/status"
	"github.com/CanadaApollo6/openmerge/internal/store"
)

type dependencyCLIJournal struct{ j store.Journal }

func (s dependencyCLIJournal) Journals(_ context.Context, _ []store.QueueKey) ([]store.Journal, error) {
	return []store.Journal{s.j}, nil
}

func TestDependencyCLIPrivacyGolden(t *testing.T) {
	var want []byte
	for i, code := range []string{"missing", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope", "dependency-unresolved"} {
		j, b, _, _ := statusfixture.StatusJournal(t, code, i+1)
		j, err := status.ProjectDependencies(j, b, nil, statusfixture.StatusSources())
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		status.Handler(dependencyCLIJournal{j}, []store.QueueKey{j.Key}, "", nil).ServeHTTP(w, httptest.NewRequest("GET", "/v1/queues/acme/client/main/prs/2?events=1", nil))
		var response status.Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := printStatus(&out, response); err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = bytes.Clone(out.Bytes())
		}
		if !bytes.Equal(want, out.Bytes()) {
			t.Fatalf("%s CLI disclosed private source:\n%s", code, &out)
		}
	}
	path := filepath.Join("testdata", "dependency-private-cli.txt")
	if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
		if err := os.WriteFile(path, want, 0644); err != nil {
			t.Fatal(err)
		}
	}
	golden, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(golden, want) {
		t.Fatal("CLI golden differs", err)
	}
}

func TestDependencyMultiCLIPrivacyGoldens(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, cobatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth-%d/cobatch-%t", depth, cobatch), func(t *testing.T) {
				baseline := map[int][]byte{}
				for _, code := range []string{"missing", "dependency-unresolved", "dependency-waiting", "dependency-closed", "dependency-stale", "dependency-landed", "dependency-unavailable", "dependency-out-of-scope"} {
					for _, phase := range []string{"queued", "building", "testing", "invalidating", "discarded"} {
						j, b, _, _, prs := statusfixture.MultiJournal(t, code, phase, depth, cobatch)
						pj, err := status.ProjectDependencies(j, b, nil, statusfixture.StatusSources())
						if err != nil {
							t.Fatal(err)
						}
						h := status.Handler(dependencyCLIJournal{pj}, []store.QueueKey{j.Key}, "", nil)
						for _, pr := range append([]int{0}, prs...) {
							path := "/v1/queues/acme/client/main"
							if pr != 0 {
								path += fmt.Sprintf("/prs/%d?events=100", pr)
							}
							w := httptest.NewRecorder()
							h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
							if w.Code != 200 {
								t.Fatal(w.Code, w.Body)
							}
							var response status.Response
							if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
								t.Fatal(err)
							}
							var out bytes.Buffer
							if err := printStatus(&out, response); err != nil {
								t.Fatal(err)
							}
							if want, ok := baseline[pr]; ok {
								if !bytes.Equal(want, out.Bytes()) {
									t.Fatalf("PR %d private %s/%s leaked:\n%s\n%s", pr, code, phase, want, out.Bytes())
								}
							} else {
								baseline[pr] = bytes.Clone(out.Bytes())
							}
						}
					}
				}
				digests := map[int]string{}
				for pr, raw := range baseline {
					digests[pr] = fmt.Sprintf("%x", sha256.Sum256(raw))
				}
				raw, err := json.Marshal(digests)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join("testdata", fmt.Sprintf("dependency-private-multi-depth-%d-cobatch-%t.json", depth, cobatch))
				if os.Getenv("OPENMERGE_UPDATE_REPORTING_GOLDENS") == "1" {
					if err := os.WriteFile(path, append(raw, '\n'), 0644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(bytes.TrimSpace(want), raw) {
					t.Fatal("multi-PR CLI golden differs", err)
				}
			})
		}
	}
}

func TestDependencyMetamorphicCLI(t *testing.T) {
	seeds := 5
	if !testing.Short() {
		seeds = 40
	}
	for _, depth := range []int{1, 4} {
		for seed := 0; seed < seeds; seed++ {
			t.Run(fmt.Sprintf("depth-%d/seed-%d", depth, seed), func(t *testing.T) {
				baseline := map[int][]byte{}
				for world := 0; world < 7; world++ {
					j, b, prs := statusfixture.MetamorphicJournal(t, int64(seed), depth, world)
					j, err := status.ProjectDependencies(j, b, nil, statusfixture.StatusSources())
					if err != nil {
						t.Fatal(err)
					}
					h := status.Handler(dependencyCLIJournal{j}, []store.QueueKey{j.Key}, "", nil)
					for _, pr := range append([]int{0}, prs...) {
						path := "/v1/queues/acme/client/main"
						if pr != 0 {
							path += fmt.Sprintf("/prs/%d?events=100", pr)
						}
						w := httptest.NewRecorder()
						h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
						if w.Code != 200 {
							t.Fatal(w.Code, w.Body)
						}
						var response status.Response
						if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						var out bytes.Buffer
						if err := printStatus(&out, response); err != nil {
							t.Fatal(err)
						}
						if want, ok := baseline[pr]; ok {
							if !bytes.Equal(want, out.Bytes()) {
								t.Fatalf("world %d PR %d differs:\n%s\n%s", world, pr, want, out.Bytes())
							}
						} else {
							baseline[pr] = bytes.Clone(out.Bytes())
						}
					}
				}
			})
		}
	}
}
