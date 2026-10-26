package main

import (
	"fmt"
	"slices"
	"strings"
)

const defaultCIWorkflow = ".github/workflows/ci.yml"

// A zero value selects the original single-workflow measurement contract.
// Explicit flag values replace that default, rather than extending it.
type ciWorkflows []string

func (w ciWorkflows) paths() []string {
	if len(w) == 0 {
		return []string{defaultCIWorkflow}
	}
	return w
}

func (w ciWorkflows) String() string { return strings.Join(w.paths(), ", ") }

func (w *ciWorkflows) Set(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("CI workflow path must not be empty")
	}
	if slices.Contains(*w, path) {
		return fmt.Errorf("duplicate CI workflow path %q", path)
	}
	*w = append(*w, path)
	return nil
}
