package config

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

func TestAuthorityPublicAgreement(t *testing.T) {
	raw, err := os.ReadFile("testdata/authority-public.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Suffix string
		Valid        bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	base := "version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"
	legacy, _, err := Load([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			data := []byte(base + tc.Suffix)
			got, _, err := Load(data)
			if (err == nil) != tc.Valid {
				t.Fatalf("Load = %v", err)
			}
			assertSchemaAgreement(t, data, err)
			if tc.Valid && !reflect.DeepEqual(got, legacy) {
				t.Fatal("explicit github changed effective config")
			}
			if tc.Name == "authority unavailable" && (err == nil || !strings.Contains(err.Error(), AuthorityActivationUnavailable)) {
				t.Fatal("missing activation barrier", err)
			}
		})
	}
}

func TestAuthorityGoverningValidation(t *testing.T) {
	commit := strings.Repeat("a", 40)
	g, err := forge.CollectGoverningFiles(t.Context(), forge.GoverningFilesRequest{CommitSHA: commit, PathsComplete: true}, func(_ context.Context, p string, _ int) (forge.GoverningFile, error) {
		f := forge.GoverningFile{Path: p, Coverage: forge.DiffCoverage{Completeness: forge.DiffComplete}}
		if p == ".openmerge.yml" || p == ".github/CODEOWNERS" {
			f.Present = true
			f.Mode = "100644"
			f.ObjectID = strings.Repeat("b", 40)
			f.Content = "* @alice\n"
		}
		return f, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resolve := classification.OwnerResolver(func(classification.Owner) cp.IdentitySet {
		return cp.IdentitySet{IDs: []string{"github.com:user:1"}, Resolved: true, Fresh: true}
	})
	for _, tc := range []struct {
		name, queues, branch string
		valid                bool
	}{
		{"default", "- name: main\n  merge_when: [check-success = ci]\n", "main", true},
		{"literal", "- name: main\n  merge_when: [check-success = ci]\n  base: trunk\n", "trunk", true},
		{"missing", "- name: releases\n  merge_when: [check-success = ci]\n  base: release/*\n", "main", false},
		{"overlap", "- name: main\n  merge_when: [check-success = ci]\n- name: all\n  merge_when: [check-success = ci]\n  base: '**'\n", "main", false},
		{"unknown default", "- name: main\n  merge_when: [check-success = ci]\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("version: 1\npolicy_authority: openmerge\nbreak_glass: {team: acme/emergency}\nqueues:\n" + tc.queues)
			c, _, err := LoadAuthorityCandidate(data)
			if err != nil {
				t.Fatal(err)
			}
			governing := g
			governing.Config = append([]forge.GoverningFile(nil), g.Config...)
			for i := range governing.Config {
				if governing.Config[i].Present {
					governing.Config[i].Content = string(data)
				}
			}
			err = c.ValidateGoverning(tc.branch, commit, governing, resolve)
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
			if tc.valid {
				if err := c.ValidateGoverning(tc.branch, commit, g, resolve); err == nil {
					t.Fatal("unrelated governing config accepted")
				}
				if err := c.ValidateGoverning(tc.branch, strings.Repeat("c", 40), governing, resolve); err == nil {
					t.Fatal("wrong governing commit accepted")
				}
				if err := c.ValidateGoverning(tc.branch, commit, governing, nil); err == nil {
					t.Fatal("unresolved owners accepted")
				}
			}
		})
	}
	if err := (AuthorityCandidate{Authority: authority.Declaration{Mode: authority.GitHub}}).ValidateGoverning("", "", forge.GoverningFiles{}, nil); err != nil {
		t.Fatal("github gained governing requirement", err)
	}
}
