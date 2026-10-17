package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// MaxWebhookBody is GitHub's 25 MB webhook payload limit.
const MaxWebhookBody = 25 << 20

// WebhookError describes a rejected request without including its body or secret.
type WebhookError struct {
	StatusCode int
	Reason     string
}

func (e *WebhookError) Error() string { return "github webhook: " + e.Reason }

// Event contains authenticated delivery metadata. The server persists DeliveryID
// for typed-payload deduplication before acting on it. On payload validation errors, an
// authenticated envelope is still returned for observation/wake hints. No
// permissions are inferred here.
type Event struct {
	DeliveryID     string
	Repo           forge.RepoRef
	InstallationID int64
	Payload        Payload
}

// Payload is the closed set of normalized notifications consumed by the server.
type Payload interface{ webhookPayload() }

// PRChanged asks the server to refresh a pull request and its base configuration.
type PRChanged struct {
	Action        string
	Number        int
	HeadSHA, Base string
}

func (PRChanged) webhookPayload() {}

// ReviewChanged asks the server to refresh exact-head approvals.
type ReviewChanged struct {
	Action                              string
	Number                              int
	HeadSHA, ReviewedSHA, Author, State string
}

func (ReviewChanged) webhookPayload() {}

// ChecksChanged asks the server to reread checks and suites, including reruns.
type ChecksChanged struct{ SHA string }

func (ChecksChanged) webhookPayload() {}

// BranchPushed includes deleted branches (Deleted is true and SHA is zero).
type BranchPushed struct {
	Ref, SHA string
	Deleted  bool
}

func (BranchPushed) webhookPayload() {}

// BranchChanged requests a full branch listing; tags do not affect queues.
type BranchChanged struct {
	Ref     string
	Deleted bool
}

func (BranchChanged) webhookPayload() {}

// PRCommentCreated defers command parsing and live authorization to the server.
type PRCommentCreated struct {
	Number                          int
	Author, Body, AuthorAssociation string
}

func (PRCommentCreated) webhookPayload() {}

// InstallationChanged covers installation and installation_repositories events.
// Repository lists may be partial; the server must reconcile installation access.
type InstallationChanged struct {
	Action                       string
	Repositories, Added, Removed []forge.RepoRef
}

func (InstallationChanged) webhookPayload() {}

// Ignored is a successful, authenticated notification outside the supported set.
type Ignored struct{ Event, Action string }

func (Ignored) webhookPayload() {}

type webhookRepository struct {
	FullName string `json:"full_name"`
}
type webhookPR struct {
	Number int `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}
type webhookEnvelope struct {
	Action       string            `json:"action"`
	Number       int               `json:"number"`
	Repository   webhookRepository `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	PullRequest webhookPR `json:"pull_request"`
	Review      struct {
		CommitID string `json:"commit_id"`
		State    string `json:"state"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	CheckRun struct {
		HeadSHA string `json:"head_sha"`
	} `json:"check_run"`
	CheckSuite struct {
		HeadSHA string `json:"head_sha"`
	} `json:"check_suite"`
	SHA     string `json:"sha"`
	State   string `json:"state"`
	Ref     string `json:"ref"`
	After   string `json:"after"`
	Deleted bool   `json:"deleted"`
	RefType string `json:"ref_type"`
	Issue   struct {
		Number      int             `json:"number"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body        string `json:"body"`
		Association string `json:"author_association"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
	Repositories []webhookRepository `json:"repositories"`
	Added        []webhookRepository `json:"repositories_added"`
	Removed      []webhookRepository `json:"repositories_removed"`
}

// VerifyAndParse accepts POST only, limits the body, and verifies the full SHA256
// digest with hmac.Equal before decoding JSON. Unknown events/actions are Ignored.
// Timing comparisons of matching-length digests are constant time; malformed
// headers are rejected without interpreting an untrusted payload.
func VerifyAndParse(r *http.Request, secret []byte) (Event, error) {
	reject := func(code int, reason string) (Event, error) {
		return Event{}, &WebhookError{StatusCode: code, Reason: reason}
	}
	if r.Method != http.MethodPost {
		return reject(405, "POST required")
	}
	if len(secret) == 0 {
		return reject(401, "webhook secret required")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBody+1))
	if err != nil {
		return reject(400, "body unreadable")
	}
	if len(body) > MaxWebhookBody {
		return reject(413, "body too large")
	}
	signature := r.Header.Get("X-Hub-Signature-256")
	if !strings.HasPrefix(signature, "sha256=") {
		return reject(401, "invalid signature")
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	if err != nil || !hmac.Equal(mac.Sum(nil), digest) {
		return reject(401, "invalid signature")
	}
	delivery := r.Header.Get("X-GitHub-Delivery")
	if delivery == "" {
		return reject(400, "delivery ID required")
	}
	kind := r.Header.Get("X-GitHub-Event")
	e := Event{DeliveryID: delivery, Payload: Ignored{Event: kind}}
	var raw webhookEnvelope
	if err = json.Unmarshal(body, &raw); err != nil {
		switch kind {
		case "pull_request", "pull_request_review", "check_run", "check_suite", "status", "push", "create", "delete", "issue_comment", "installation", "installation_repositories":
			return reject(400, "invalid JSON")
		default:
			return e, nil
		}
	}
	e.Repo = forge.RepoRef{Path: raw.Repository.FullName}
	e.InstallationID = raw.Installation.ID
	e.Payload = Ignored{Event: kind, Action: raw.Action}
	switch kind {
	case "pull_request":
		switch raw.Action {
		case "opened", "synchronize", "reopened", "closed", "edited", "labeled", "unlabeled":
			e.Payload = PRChanged{Action: raw.Action, Number: raw.Number, HeadSHA: raw.PullRequest.Head.SHA, Base: raw.PullRequest.Base.Ref}
		}
	case "pull_request_review":
		switch raw.Action {
		case "submitted", "dismissed":
			e.Payload = ReviewChanged{Action: raw.Action, Number: raw.PullRequest.Number, HeadSHA: raw.PullRequest.Head.SHA, ReviewedSHA: raw.Review.CommitID, Author: raw.Review.User.Login, State: raw.Review.State}
		}
	case "check_run":
		if raw.Action == "completed" || raw.Action == "rerequested" {
			e.Payload = ChecksChanged{SHA: raw.CheckRun.HeadSHA}
		}
	case "check_suite":
		if raw.Action == "completed" || raw.Action == "requested" || raw.Action == "rerequested" {
			e.Payload = ChecksChanged{SHA: raw.CheckSuite.HeadSHA}
		}
	case "status":
		switch raw.State {
		case "success", "failure", "error":
			e.Payload = ChecksChanged{SHA: raw.SHA}
		}
	case "create", "delete":
		if raw.RefType == "branch" {
			e.Payload = BranchChanged{Ref: raw.Ref, Deleted: kind == "delete"}
		}
	case "push":
		if strings.HasPrefix(raw.Ref, "refs/heads/") {
			e.Payload = BranchPushed{Ref: raw.Ref, SHA: raw.After, Deleted: raw.Deleted}
		}
	case "issue_comment":
		if raw.Action == "created" && len(raw.Issue.PullRequest) > 0 && string(raw.Issue.PullRequest) != "null" {
			e.Payload = PRCommentCreated{Number: raw.Issue.Number, Author: raw.Comment.User.Login, Body: raw.Comment.Body, AuthorAssociation: raw.Comment.Association}
		}
	case "installation", "installation_repositories":
		e.Payload = InstallationChanged{Action: raw.Action, Repositories: repoRefs(raw.Repositories), Added: repoRefs(raw.Added), Removed: repoRefs(raw.Removed)}
	}
	if err = validateEvent(e); err != nil {
		return e, &WebhookError{StatusCode: 400, Reason: "incomplete event payload"}
	}
	return e, nil
}
func repoRefs(list []webhookRepository) []forge.RepoRef {
	refs := make([]forge.RepoRef, 0, len(list))
	for _, repo := range list {
		refs = append(refs, forge.RepoRef{Path: repo.FullName})
	}
	return refs
}
func validateEvent(e Event) error {
	switch p := e.Payload.(type) {
	case Ignored:
		return nil
	case InstallationChanged:
		if e.InstallationID > 0 && p.Action != "" {
			return nil
		}
	case PRChanged:
		if p.Number > 0 && p.HeadSHA != "" && p.Base != "" {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	case ReviewChanged:
		if p.Number > 0 && p.HeadSHA != "" {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	case ChecksChanged:
		if p.SHA != "" {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	case BranchChanged:
		if p.Ref != "" && e.InstallationID > 0 {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	case BranchPushed:
		if p.SHA != "" {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	case PRCommentCreated:
		if p.Number > 0 && p.Author != "" {
			_, _, err := splitRepo(e.Repo)
			return err
		}
	}
	return errors.New("incomplete event")
}
