package gitops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// provenanceIndexBytes sizes only local, regular index files. Root confines all
// path resolution (including symlinks) to gitDir; no Git subprocess or inherited
// Git environment participates in sizing. As with object reads, callers must
// keep the already-acquired repository stable for the duration of the read.
func provenanceIndexBytes(ctx context.Context, gitDir string) (int, error) {
	root, err := os.OpenRoot(gitDir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	// Git would discover object directories we have not sized through either
	// mechanism. Fail closed even for an empty file or a dangling symlink.
	for _, name := range []string{"commondir", "objects/info/alternates"} {
		if _, err := root.Lstat(name); err == nil {
			return 0, errors.New("provenance reader requires a self-contained object directory")
		} else if !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	pack, err := root.Open("objects/pack")
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = pack.Close() }()
	var total int64
	add := func(n int64) { total = min(int64(provenanceProcessCeiling), total+n) }
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		entries, err := pack.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for _, entry := range entries {
			name := entry.Name()
			idx := strings.HasSuffix(name, ".idx")
			if !idx && !strings.HasSuffix(name, ".rev") && name != "multi-pack-index" {
				continue
			}
			path := filepath.Join("objects/pack", name)
			info, err := root.Stat(path)
			if err != nil {
				return 0, err
			}
			if !info.Mode().IsRegular() {
				return 0, errors.New("provenance index is not a regular file")
			}
			// Saturate before arithmetic so even enormous sparse files cannot
			// overflow the cap. Keep inspecting paths after saturation.
			size := min(info.Size(), int64(provenanceProcessCeiling))
			add(size)
			if idx {
				rev, err := root.Stat(strings.TrimSuffix(path, ".idx") + ".rev")
				if errors.Is(err, os.ErrNotExist) {
					// Git's heap reverse index costs about 12 bytes/object.
					// An idx uses at least 24 bytes/object (SHA-1 v1;
					// v2 and SHA-256 use more). Round up, conservatively
					// including headers and any large-offset table.
					add(((size + 23) / 24) * 12)
				} else if err != nil {
					return 0, err
				} else if !rev.Mode().IsRegular() {
					return 0, errors.New("provenance reverse index is not a regular file")
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return int(total), nil
		}
	}
}
