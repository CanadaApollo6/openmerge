package queue

// LandingCleanupPolicyRecorded pins the cleanup choice immediately after
// LandingStarted and before the landing push. The first decision wins.
type LandingCleanupPolicyRecorded struct {
	BatchID, AttemptID string
	DeleteHeadBranch   bool
}

func (LandingCleanupPolicyRecorded) queueEvent() {}

// Validate correlates the decision with one durable landing attempt.
func (e LandingCleanupPolicyRecorded) Validate() error {
	return requireActionText("batch_id", e.BatchID, "attempt_id", e.AttemptID)
}

type wireLandingCleanupPolicyRecorded struct {
	BatchID          string `json:"batch_id"`
	AttemptID        string `json:"attempt_id"`
	DeleteHeadBranch bool   `json:"delete_head_branch"`
}
