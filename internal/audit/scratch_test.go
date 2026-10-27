package audit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScratchBudgetReservesBeforeWrite(t *testing.T) {
	budget, err := newScratchBudget(ScratchLimits{MaxBytes: 8192, MaxFiles: 2})
	must(t, err)
	path := filepath.Join(t.TempDir(), "entry")
	must(t, budget.writeFile(path, make([]byte, 4097)))
	if err := budget.writeFile(path, make([]byte, 8193)); !errors.Is(err, ErrScratchLimit) {
		t.Fatalf("oversized replacement accepted: %v", err)
	}
	info, err := os.Stat(path)
	must(t, err)
	if info.Size() != 4097 {
		t.Fatal("quota failure changed the file")
	}
	// Replacing an entry, including a duplicate marker, releases its old charge
	// without leaking an inode reservation or retaining an in-memory path map.
	must(t, budget.writeFile(path, nil))
	must(t, budget.writeFile(filepath.Join(filepath.Dir(path), "second"), nil))
	if err := budget.writeFile(filepath.Join(filepath.Dir(path), "third"), nil); !errors.Is(err, ErrScratchLimit) {
		t.Fatalf("file count exceeded: %v", err)
	}
	if budget.bytes != 8192 || budget.files != 2 {
		t.Fatalf("incorrect accounting: %+v", budget)
	}
}
