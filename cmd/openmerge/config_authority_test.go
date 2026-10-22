package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigValidateAuthorityPublicCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../internal/config/testdata/authority-public-activation.json")
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
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte("version: 1\nqueues:\n- name: main\n  merge_when: [check-success = ci]\n"+tc.Suffix), 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostic bytes.Buffer
			code := validateConfig([]string{path}, &out, &diagnostic)
			if (code == 0) != tc.Valid {
				t.Fatalf("exit=%d stderr=%s", code, diagnostic.String())
			}
		})
	}
}
