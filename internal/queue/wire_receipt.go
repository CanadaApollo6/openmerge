package queue

import (
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
	"time"
)

type wireReceiptIdentity struct {
	ChangeID     string `json:"change_id"`
	Revision     uint64 `json:"revision"`
	Generation   uint64 `json:"generation"`
	Base         string `json:"base"`
	Head         string `json:"head"`
	MergeBase    string `json:"merge_base"`
	IntentDigest string `json:"intent_digest"`
	PolicyDigest string `json:"policy_digest"`
	BriefDigest  string `json:"brief_digest"`
}

type wireReceiptActor struct {
	ID    string         `json:"id"`
	Login string         `json:"login"`
	Kind  cp.AccountKind `json:"kind"`
}

type wireApprovalReceiptRecorded struct {
	Basis      ApprovalBasis       `json:"basis"`
	Supplied   wireReceiptIdentity `json:"supplied"`
	Current    wireReceiptIdentity `json:"current"`
	Actor      wireReceiptActor    `json:"actor"`
	CommentID  string              `json:"comment_id"`
	BoundaryAt time.Time           `json:"boundary_at"`
	CreatedAt  time.Time           `json:"created_at"`
	Accepted   bool                `json:"accepted"`
	ReasonCode string              `json:"reason_code"`
}

func receiptToWire(e ApprovalReceiptRecorded) wireApprovalReceiptRecorded {
	return wireApprovalReceiptRecorded{e.Basis, wireReceiptIdentity(e.Supplied), wireReceiptIdentity(e.Current), wireReceiptActor(e.Actor), e.CommentID, e.BoundaryAt, e.CreatedAt, e.Accepted, e.ReasonCode}
}
func (w wireApprovalReceiptRecorded) event() ApprovalReceiptRecorded {
	return ApprovalReceiptRecorded{w.Basis, cp.ReceiptIdentity(w.Supplied), cp.ReceiptIdentity(w.Current), cp.Identity(w.Actor), w.CommentID, w.BoundaryAt, w.CreatedAt, w.Accepted, w.ReasonCode}
}
