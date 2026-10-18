package classification

import (
	"fmt"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"reflect"
)

// RefreshInputs excludes governing commit/config blob provenance (Q7).
// Owner/attribute maps contain object IDs or explicit "absent" proofs, never
// missing probes disguised as absence. The caller owns semantic policy hashing.
type RefreshInputs struct {
	PolicyDigest, ClassifierVersion string
	Owners, Attributes              map[string]string
}

// RefreshReasons uses the caller's ancestry observation, never I/O. Forward
// predecessor landings preserve the original base pin; false includes rewinds
// and unknown ancestry, conservatively requiring a fresh diff. Started landing
// attempts retain their frozen snapshot outside this component.
func RefreshReasons(old, next RefreshInputs, baseChanged, forward bool) []string {
	var reasons []string
	if baseChanged && !forward {
		reasons = append(reasons, "base_rewrite")
	}
	if old.PolicyDigest != next.PolicyDigest {
		reasons = append(reasons, "semantic_policy")
	}
	if old.ClassifierVersion != next.ClassifierVersion {
		reasons = append(reasons, "classifier_version")
	}
	if !reflect.DeepEqual(old.Owners, next.Owners) {
		reasons = append(reasons, "governing_owners")
	}
	if !reflect.DeepEqual(old.Attributes, next.Attributes) {
		reasons = append(reasons, "governing_attributes")
	}
	return reasons
}

// NextGeneration advances even when a rewrite produces identical diff evidence.
// Provenance-only refreshes preserve the generation and hence receipt bindings.
func NextGeneration(previous uint64, reasons []string) (uint64, error) {
	if previous == 0 {
		return 1, nil
	}
	if len(reasons) == 0 {
		return previous, nil
	}
	if previous == ^uint64(0) {
		return 0, fmt.Errorf("classification generation exhausted")
	}
	return previous + 1, nil
}

// SemanticInputs projects only invalidation inputs. Checked absences and failed
// probes have distinct markers. Content is immutable under its object ID.
func SemanticInputs(e Evidence) RefreshInputs {
	out := RefreshInputs{PolicyDigest: e.Classification.Inputs.PolicyDigest, ClassifierVersion: e.Classification.Inputs.ClassifierVersion, Owners: map[string]string{}, Attributes: map[string]string{}}
	out.Owners = probeIdentities(e.Governing.Codeowners)
	out.Attributes = probeIdentities(e.Governing.Attributes)
	return out
}

func probeIdentities(files []forge.GoverningFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		value := "unknown:" + f.ObjectID
		if f.Coverage.Completeness == forge.DiffComplete {
			value = "absent"
			if f.Present {
				value = f.ObjectID
			}
		}
		out[f.Path] = value
	}
	return out
}
