package queue

import "fmt"

// LifecycleChanged controls admission and new starts independently of operator
// pauses. Existing logs default to active; landing outcomes remain fence-driven.
type LifecycleChanged struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
}

func (LifecycleChanged) queueEvent() {}

// Validate rejects unknown states and requires a diagnostic for blocked queues.
func (e LifecycleChanged) Validate() error {
	switch e.State {
	case "active":
		return nil
	case "dormant", "retired", "overlapping":
		return requireActionText("detail", e.Detail)
	default:
		return fmt.Errorf("invalid queue lifecycle %q", e.State)
	}
}

// LifecycleState preserves the active interpretation of pre-lifecycle logs.
func (s State) LifecycleState() string {
	if s.Lifecycle.State == "" {
		return "active"
	}
	return s.Lifecycle.State
}

func (s State) lifecycleBlocked() bool { return s.LifecycleState() != "active" }
