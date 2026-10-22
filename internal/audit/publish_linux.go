//go:build linux

package audit

import (
	"runtime"
	"syscall"
	"unsafe"
)

const openInputFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// Linux renameat2(RENAME_NOREPLACE) publishes the complete directory in one
// operation. Unsupported architectures/filesystems fail closed; no rename fallback.
func renameNoReplace(from, to string) error {
	var number uintptr
	switch runtime.GOARCH {
	case "amd64":
		number = 316
	case "arm64", "riscv64", "loong64":
		number = 276
	case "386":
		number = 353
	default:
		return errOutput
	}
	a, e := syscall.BytePtrFromString(from)
	if e != nil {
		return errOutput
	}
	b, e := syscall.BytePtrFromString(to)
	if e != nil {
		return errOutput
	}
	const atFDCWD = ^uintptr(99)
	_, _, errno := syscall.Syscall6(number, atFDCWD, uintptr(unsafe.Pointer(a)), atFDCWD, uintptr(unsafe.Pointer(b)), 1, 0)
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	if errno != 0 {
		return errOutput
	}
	return nil
}
