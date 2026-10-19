package changepolicy

// PolicyPin preserves static identity inputs unavailable from Git. Membership
// and grants are deliberately excluded; decisions always acquire them afresh.
type PolicyPin struct {
	DefaultBranch     string             `json:"default_branch"`
	Resolved          []ResolvedIdentity `json:"resolved"`
	ClassifierVersion string             `json:"classifier_version"`
}
