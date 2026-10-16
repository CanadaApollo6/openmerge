package queue

// PRMetadataObserved records the non-CI condition inputs alongside PRObserved's
// evaluated eligibility. Admission still uses PRObserved. A changed observation
// invalidates the active member's merge-policy result until checks are refreshed.
// Unknown lists are empty and their Known flag is false.
type PRMetadataObserved struct {
	PR                                 int
	HeadSHA, Base, Head, Title, Author string
	Draft                              bool
	ApprovedReviewsBy, Labels, Files   []string
	LabelsKnown, FilesKnown            bool
	ConflictKnown, Conflict            bool
}

func (PRMetadataObserved) queueEvent() {}

type wirePRMetadataObserved struct {
	PR                int      `json:"pr"`
	HeadSHA           string   `json:"head_sha"`
	Base              string   `json:"base"`
	Head              string   `json:"head"`
	Title             string   `json:"title"`
	Author            string   `json:"author"`
	Draft             bool     `json:"draft"`
	ApprovedReviewsBy []string `json:"approved_reviews_by"`
	Labels            []string `json:"labels"`
	Files             []string `json:"files"`
	LabelsKnown       bool     `json:"labels_known"`
	FilesKnown        bool     `json:"files_known"`
	ConflictKnown     bool     `json:"conflict_known"`
	Conflict          bool     `json:"conflict"`
}
