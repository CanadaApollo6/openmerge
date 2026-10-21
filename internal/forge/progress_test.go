package forge

import (
	"context"
	"testing"
)

func TestProgressHookScope(t *testing.T) {
	parent := context.Background()
	calls := 0
	ctx := WithProgressHook(parent, func() { calls++ })
	NotifyProgress(parent)
	NotifyProgress(WithProgressHook(parent, nil))
	if calls != 0 {
		t.Fatal("hook leaked to parent")
	}
	NotifyProgress(ctx)
	if calls != 1 {
		t.Fatal("missing notification")
	}
}
