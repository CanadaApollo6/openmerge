package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ConfigApplied pins the default-branch commit and effective queue policy.
// A changed digest invalidates test work, but cannot settle a started landing.
type ConfigApplied struct {
	Commit, Digest string
	Window         *WindowBounds
}

func (ConfigApplied) queueEvent() {}

// Validate requires an authority commit and a SHA-256 policy digest.
func (e ConfigApplied) Validate() error {
	if err := requireActionText("commit", e.Commit); err != nil {
		return err
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
}
