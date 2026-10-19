package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
)

// ConfigApplied pins the default-branch commit and effective queue policy.
// A changed digest invalidates test work, but cannot settle a started landing.
type ConfigApplied struct {
	Commit, Digest string
	Window         *WindowBounds
	Policy         *cp.PolicyPin
}

func (ConfigApplied) queueEvent() {}

// Validate requires an authority commit and a SHA-256 policy digest.
func (e ConfigApplied) Validate() error {
	if err := requireActionText("commit", e.Commit); err != nil {
		return err
	}
	if e.Policy != nil {
		if e.Policy.DefaultBranch == "" || len(e.Policy.DefaultBranch) > 1024 || e.Policy.ClassifierVersion == "" || len(e.Policy.ClassifierVersion) > 128 || e.Policy.Resolved == nil || len(e.Policy.Resolved) > cp.MaxPolicyIdentities {
			return fmt.Errorf("invalid approval policy pin")
		}
		seen := map[string]bool{}
		for _, r := range e.Policy.Resolved {
			if r.Name == "" || len(r.Name) > 256 || r.ID == "" || len(r.ID) > 256 || seen[r.Name] {
				return fmt.Errorf("invalid approval identity pin")
			}
			seen[r.Name] = true
		}
	}
	if e.Window != nil {
		if err := e.Window.validate(); err != nil {
			return err
		}
	}
	digest, err := hex.DecodeString(e.Digest)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("digest must be a SHA-256 hex digest")
	}
	return nil
}

type wireConfigApplied struct {
	Commit string        `json:"commit"`
	Digest string        `json:"digest"`
	Window *WindowBounds `json:"window,omitempty"`
	Policy *cp.PolicyPin `json:"policy,omitempty"`
}
