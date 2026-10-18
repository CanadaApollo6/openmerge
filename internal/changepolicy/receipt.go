package changepolicy

// ReceiptIdentity is the shared binding value. PR 7a owns canonical brief
// hashing, receipt acceptance, validation and wire records; this is not a codec.
// Change/revision primitives are converted at the queue boundary, avoiding a
// dependency on queue. Generation changes on a base rewrite, even at equal heads.
type ReceiptIdentity struct {
	ChangeID                                      string
	Revision, Generation                          uint64
	Head, IntentDigest, PolicyDigest, BriefDigest string
}

func (r ReceiptIdentity) recorded() bool {
	return r.ChangeID != "" && r.Revision > 0 && r.Generation > 0 && r.Head != "" &&
		r.IntentDigest != "" && r.PolicyDigest != "" && r.BriefDigest != ""
}
