package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/queue"
)

func TestMultipleQueuesAndRepositorySettings(t *testing.T) {
	data := []byte(`version: 1
delete_head_branch: false
queues:
  - name: default
    merge_when: &checks [check-success = ci]
  - name: releases
    base: release/*
    merge_when: *checks
    merge_method: merge
  - name: maintenance
    base: maintenance/**
    merge_when: *checks
    merge_method: rebase
`)
	cfg, warnings, err := Load(data)
	assertSchemaAgreement(t, data, err)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %+v, %v, %v", cfg, warnings, err)
	}
	if cfg.Version != 1 || len(cfg.Queues) != 3 || cfg.DeleteHeadBranch == nil || *cfg.DeleteHeadBranch {
		t.Fatalf("config = %+v", cfg)
	}
	for _, tc := range []struct {
		branch, name string
		method       queue.MergeMethod
	}{
		{"main", "default", queue.Squash},
		{"release/v1", "releases", queue.Merge},
		{"maintenance/old/v1", "maintenance", queue.Rebase},
	} {
		q, diagnostics := cfg.ResolveBranch(tc.branch, "main")
		if len(diagnostics) != 0 || q.Name != tc.name || q.MergeMethod != tc.method {
			t.Errorf("ResolveBranch(%q) = %+v, %v", tc.branch, q, diagnostics)
		}
	}
}

func TestDeleteHeadBranchOptionalBoolean(t *testing.T) {
	for _, setting := range []string{"", "delete_head_branch: true\n", "delete_head_branch: false\n"} {
		t.Run(strings.TrimSpace(setting), func(t *testing.T) {
			data := []byte("version: 1\n" + setting + "queues: [{name: default, merge_when: [check-success = ci]}]\n")
			cfg, _, err := Load(data)
			assertSchemaAgreement(t, data, err)
			if err != nil {
				t.Fatal(err)
			}
			if setting == "" {
				if cfg.DeleteHeadBranch != nil || cfg.Queues[0].Base != "" {
					t.Fatal("legacy file lost its dynamic repository/serve defaults")
				}
			} else if cfg.DeleteHeadBranch == nil || *cfg.DeleteHeadBranch != strings.Contains(setting, "true") {
				t.Fatalf("setting %q = %v", setting, cfg.DeleteHeadBranch)
			}
		})
	}
}

func TestBaseSelectors(t *testing.T) {
	for _, tc := range []struct {
		selector, branch, defaultBranch string
		want                            bool
	}{
		{"", "trunk", "trunk", true},
		{"", "main", "trunk", false},
		{"release/v1", "release/v1", "main", true},
		{"release/v1", "release/v10", "main", false},
		{"release/*", "release/v1", "main", true},
		{"release/*", "release/old/v1", "main", false},
		{"release/**", "release/old/v1", "main", true},
		{"**/main", "main", "trunk", true},
		{"**/main", "release/main", "trunk", true},
		{"release/v?", "release/v1", "main", true},
		{"release/v?", "release/v10", "main", false},
		{`release/\*`, "release/*", "main", true},
		{`release/\*`, "release/v1", "main", false},
		{"release/[12]", "release/1", "main", false},
		{"release/[12]", "release/[12]", "main", true},
	} {
		if got := (Queue{Base: tc.selector}).MatchesBase(tc.branch, tc.defaultBranch); got != tc.want {
			t.Errorf("selector %q branch %q default %q = %v", tc.selector, tc.branch, tc.defaultBranch, got)
		}
	}
}

func TestOverlappingBasesHavePerQueueDiagnostics(t *testing.T) {
	data := []byte(`version: 1
queues:
  - name: default
    merge_when: &checks [check-success = ci]
  - name: releases
    base: release/**
    merge_when: *checks
  - name: v1
    base: release/v1
    merge_when: *checks
`)
	cfg, _, err := Load(data)
	assertSchemaAgreement(t, data, err)
	if err != nil {
		t.Fatal(err)
	}
	q, diagnostics := cfg.ResolveBranch("release/v1", "main")
	if !reflect.DeepEqual(q, Queue{}) || len(diagnostics) != 2 {
		t.Fatalf("overlap = %+v, %v", q, diagnostics)
	}
	for i, want := range []struct {
		line int
		path string
	}{{6, "queues[1].base"}, {9, "queues[2].base"}} {
		d := diagnostics[i]
		if d.Line != want.line || d.Path != want.path || !strings.Contains(d.Message, `branch "release/v1"`) || !strings.Contains(d.Message, "releases, v1") {
			t.Errorf("diagnostic = %+v", d)
		}
	}
	for _, branch := range []string{"main", "release/v2"} {
		if _, diagnostics := cfg.ResolveBranch(branch, "main"); len(diagnostics) != 0 {
			t.Fatalf("unaffected branch %q failed: %v", branch, diagnostics)
		}
	}
	if _, diagnostics := cfg.ResolveBranch("unconfigured", "main"); len(diagnostics) != 1 || !strings.Contains(diagnostics.Error(), "no queue matches") {
		t.Fatalf("unconfigured branch = %v", diagnostics)
	}
	// An omitted selector participates in overlaps after default-branch resolution.
	cfg.Queues[1].Base = "main"
	if _, diagnostics := cfg.ResolveBranch("main", "main"); len(diagnostics) != 2 || diagnostics[0].Path != "queues[0].base" {
		t.Fatalf("default/literal overlap = %v", diagnostics)
	}
}

func TestDuplicateQueueNamesAreLoaderValidation(t *testing.T) {
	// JSON Schema cannot express uniqueness of one property across array items.
	// This documented semantic check is additional to the schema, like overlaps.
	data := []byte("version: 1\nqueues:\n- &queue {name: duplicate, base: main, merge_when: [check-success = ci]}\n- {<<: *queue, base: release/*}\n")
	cfg, _, err := Load(data)
	if err == nil || !strings.Contains(err.Error(), `line 4: queues[1].name: duplicate queue name "duplicate"`) || !reflect.DeepEqual(cfg, Config{}) {
		t.Fatalf("duplicate names = %+v, %v", cfg, err)
	}
	assertSchemaAgreement(t, data, nil)
}
