//go:build !unix

package gitops

import "os/exec"

// Other platforms retain CommandContext's direct-process cancellation.
func configureProcessGroup(_ *exec.Cmd) {}
