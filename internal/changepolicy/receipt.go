package changepolicy

// ReceiptIdentity is the shared binding value. PR 7a owns canonical brief
// hashing, receipt acceptance, validation and wire records; this is not a codec.
// Change/revision primitives are converted at the queue boundary, avoiding a
// dependency on queue. Generation changes on a base rewrite, even at equal heads.
// Revisions and generations start at 1; zero means not recorded.
type ReceiptIdentity struct {
	ChangeID                                string
	Revision, Generation                    uint64
	Base, Head, MergeBase                   string
	IntentDigest, PolicyDigest, BriefDigest string
}

// Recorded reports whether every receipt binding component is present.
func (r ReceiptIdentity) Recorded() bool {
	return r.ChangeID != "" && r.Revision > 0 && r.Generation > 0 && r.Base != "" && r.Head != "" && r.MergeBase != "" &&
		r.IntentDigest != "" && r.PolicyDigest != "" && r.BriefDigest != ""
}
