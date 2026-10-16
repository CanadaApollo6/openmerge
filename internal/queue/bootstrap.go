package queue

// FenceInitialized records deferred bootstrap after startup permission/rules
// diagnostics. It can only fill an absent fence; it never replaces a token.
type FenceInitialized struct{ Token string }

func (FenceInitialized) queueEvent() {}

// Validate requires an observed initial fence token.
func (e FenceInitialized) Validate() error { return requireActionText("token", e.Token) }

type wireFenceInitialized struct {
	Token string `json:"token"`
}
