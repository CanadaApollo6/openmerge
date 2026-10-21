package forge

import "context"

type progressHookKey struct{}

// WithProgressHook attaches a request-completion callback scoped to this caller.
// The callback must be safe for concurrent use and must not block.
func WithProgressHook(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, progressHookKey{}, hook)
}

// NotifyProgress reports a successful, fully consumed request. Without a hook it
// is a no-op; drivers must not call it for failed or still-streaming responses.
func NotifyProgress(ctx context.Context) {
	if hook, _ := ctx.Value(progressHookKey{}).(func()); hook != nil {
		hook()
	}
}
