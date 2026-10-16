package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/CanadaApollo6/openmerge/internal/forge/fake"
)

func TestReadFile(t *testing.T) {
	f := fake.New()
	base, _ := f.Init(repo, "main", fenceRef)
	pr, err := f.OpenPRWithFiles(repo, 1, base, true, map[string]string{".openmerge.yml": "queue: main\n"})
	must(t, err)
	must(t, f.PushRef(repo, "config", pr.HeadSHA))
	for _, ref := range []string{"config", "refs/heads/config", pr.HeadSHA} {
		contents, err := f.ReadFile(context.Background(), repo, ref, ".openmerge.yml")
		must(t, err)
		if string(contents) != "queue: main\n" {
			t.Fatalf("contents=%q", contents)
		}
		contents[0] = 'X'
	}
	for _, ref := range []string{"main", "missing"} {
		if _, err = f.ReadFile(context.Background(), repo, ref, ".openmerge.yml"); err == nil {
			t.Fatal("missing file accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = f.ReadFile(ctx, repo, "config", ".openmerge.yml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v", err)
	}
}
