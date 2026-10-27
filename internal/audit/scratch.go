package audit

import (
	"errors"
	"fmt"
	"os"
)

// DefaultScratchMaxBytes and DefaultScratchMaxFiles bound provenance index and
// fold storage per export or replay. Other spools and output scale with selection.
const (
	DefaultScratchMaxBytes int64 = 256 << 20
	DefaultScratchMaxFiles int64 = 65536
)

// ScratchLimits includes files and directories. Bytes are charged in at least
// 4 KiB units per entry, before writing. Zero fields select the defaults.
type ScratchLimits struct {
	MaxBytes int64
	MaxFiles int64
}

// ErrScratchLimit is terminal: staging is discarded and no bundle is published.
var ErrScratchLimit = errors.New("audit: provenance scratch limit exceeded")

// ScratchLimitError identifies the exhausted resource without exposing paths.
type ScratchLimitError struct {
	Resource string
	Limit    int64
}

func (e *ScratchLimitError) Error() string {
	return fmt.Sprintf("%v: %s limit %d", ErrScratchLimit, e.Resource, e.Limit)
}
func (e *ScratchLimitError) Unwrap() error { return ErrScratchLimit }

type scratchBudget struct {
	limits ScratchLimits
	bytes  int64
	files  int64
}

type scratchBudgetKey struct{}

func newScratchBudget(limits ScratchLimits) (*scratchBudget, error) {
	if limits.MaxBytes < 0 || limits.MaxFiles < 0 {
		return nil, ErrSelectors
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = DefaultScratchMaxBytes
	}
	if limits.MaxFiles == 0 {
		limits.MaxFiles = DefaultScratchMaxFiles
	}
	return &scratchBudget{limits: limits}, nil
}

func scratchCharge(size int64) int64 {
	return max(int64(4096), (size+4095)/4096*4096)
}

// reserve accounts for the replacement size, without keeping a path map in RAM.
// All callers abort the export on a failed reservation or write.
func (b *scratchBudget) reserve(path string, size int64) error {
	old, files := int64(0), int64(1)
	info, err := os.Stat(path)
	if err == nil {
		old, files = scratchCharge(info.Size()), 0
	} else if !os.IsNotExist(err) {
		return errOutput
	}
	return b.add(scratchCharge(size)-old, files)
}

func (b *scratchBudget) add(delta, files int64) error {
	if files > b.limits.MaxFiles-b.files {
		return &ScratchLimitError{Resource: "files", Limit: b.limits.MaxFiles}
	}
	if delta > b.limits.MaxBytes-b.bytes {
		return &ScratchLimitError{Resource: "bytes", Limit: b.limits.MaxBytes}
	}
	b.bytes += delta
	b.files += files
	return nil
}

func (b *scratchBudget) mkdir(path string) error {
	if err := b.reserve(path, 0); err != nil {
		return err
	}
	if os.Mkdir(path, 0700) != nil {
		return errOutput
	}
	return nil
}

func (b *scratchBudget) writeFile(path string, raw []byte) error {
	if err := b.reserve(path, int64(len(raw))); err != nil {
		return err
	}
	if os.WriteFile(path, raw, 0600) != nil {
		return errOutput
	}
	return nil
}

func (b *scratchBudget) mkdirTemp(dir, prefix string) (string, error) {
	if err := b.add(scratchCharge(0), 1); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(dir, prefix)
	if err != nil {
		return "", errOutput
	}
	return path, nil
}
