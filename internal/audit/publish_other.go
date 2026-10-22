//go:build !linux

package audit

const openInputFlags = 0

// Fail closed until this platform has an atomic no-replace directory primitive.
func renameNoReplace(string, string) error { return errOutput }
