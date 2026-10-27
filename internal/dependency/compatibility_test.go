package dependency

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/config"
)

type configVector struct {
	Source   string
	Parsed   json.RawMessage
	Warnings json.RawMessage
	Error    string
	Digests  []string
}

func configResult(source string) configVector {
	out := configVector{Source: source}
	cfg, warnings, err := config.Load([]byte(source))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	// Config and Diagnostic contain only supported JSON values.
	out.Parsed, _ = json.Marshal(cfg)
	out.Warnings, _ = json.Marshal(warnings)
	for _, q := range cfg.Queues {
		out.Digests = append(out.Digests, q.Digest(false), q.Digest(true))
	}
	return out
}

// The golden records were captured with the untouched origin/main loader at
// 83544217e862a9e8fba85cdaf9bd3e4660732e31. Compare parsed bytes, warnings,
// errors and both cleanup-policy digest variants, not just successful loading.
func TestLegacyConfigParsingAndDigestsByteIdentical(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-config.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []configVector
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, want := range vectors {
		got := configResult(want.Source)
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		// RawMessage is compacted during marshaling on both sides.
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("legacy result changed for %q\ngot %s\nwant %s", want.Source, gotJSON, wantJSON)
		}
	}
}
