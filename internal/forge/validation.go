package forge

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidInput identifies a locally rejected forge operation, before any
// remote mutation. It is distinct from a lease refusal or a merge conflict.
var ErrInvalidInput = errors.New("invalid forge input")

func invalid(message string) error { return fmt.Errorf("%s: %w", message, ErrInvalidInput) }

// ValidateRefName implements git check-ref-format without --allow-onelevel or
// --refspec-pattern. Keep its executable parity test when changing these rules.
func ValidateRefName(ref string) error {
	if !strings.Contains(ref, "/") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{") {
		return invalid("invalid git ref")
	}
	for _, c := range []byte(ref) {
		if c <= ' ' || c == 127 || strings.ContainsRune("~^:?*[\\", rune(c)) {
			return invalid("invalid git ref")
		}
	}
	for part := range strings.SplitSeq(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return invalid("invalid git ref")
		}
	}
	return nil
}

// BranchRef accepts a plain branch or a fully qualified refs/heads name.
func BranchRef(ref string) (string, error) {
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return "", invalid("expected a branch ref")
	}
	return ref, ValidateRefName(ref)
}

// DeletionRef matches the GitHub API's branch/tag shorthand normalization.
func DeletionRef(ref string) (string, error) {
	ref = strings.TrimPrefix(ref, "refs/")
	if !strings.HasPrefix(ref, "heads/") && !strings.HasPrefix(ref, "tags/") {
		ref = "heads/" + ref
	}
	return ref, ValidateRefName("refs/" + ref)
}

// ValidateCommitIDs requires full SHA-1 or SHA-256 hexadecimal object IDs.
func ValidateCommitIDs(shas ...string) error {
	for _, sha := range shas {
		if !ValidDiffObjectID(sha) {
			return invalid("expected a full git commit SHA")
		}
	}
	return nil
}

// ValidateDeleteBranch requires a plain, non-queue branch and an exact lease.
func ValidateDeleteBranch(branch, sha string) error {
	if branch == "" || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "openmerge/") {
		return invalid("cleanup requires a plain non-OpenMerge branch and pinned SHA")
	}
	if err := ValidateCommitIDs(sha); err != nil {
		return err
	}
	_, err := BranchRef(branch)
	return err
}

// ValidateCommentKey protects the hidden HTML idempotency marker.
func ValidateCommentKey(key string) error {
	if key == "" || strings.ContainsAny(key, "<>\r\n") || strings.Contains(key, "--") {
		return invalid("invalid OpenMerge comment key")
	}
	return nil
}

// ValidateCanWriteLogin preserves the legacy permission endpoint's input rules.
// Stable identity reads use their own stricter account validation.
func ValidateCanWriteLogin(user string) error {
	if user == "" || strings.ContainsAny(user, "/\\?#%\r\n") {
		return invalid("invalid forge login")
	}
	return nil
}

// ValidateReadFile requires an explicit revision and path.
func ValidateReadFile(ref, path string) error {
	if ref == "" || path == "" {
		return invalid("ReadFile requires ref and path")
	}
	return nil
}

// ValidateStatus rejects states that have no forge check mapping.
func ValidateStatus(s Status) error {
	switch s.State {
	case Queued, Testing, Landing, Merged, Dequeued, Blocked, Paused:
		return nil
	}
	return invalid("unknown OpenMerge status state")
}

// ValidateFenceID requires the single-token identity used in fence messages.
func ValidateFenceID(id string) error {
	if id == "" || strings.ContainsAny(id, " \t\r\n\x00") {
		return invalid("single-token fence ID is required")
	}
	return nil
}

// ValidateBuildPRs checks pins before consulting remote PR heads.
func ValidateBuildPRs(prs []BuildPR) error {
	for _, pr := range prs {
		if pr.Number <= 0 {
			return invalid("positive PR number is required")
		}
		if err := ValidateCommitIDs(pr.HeadSHA); err != nil {
			return err
		}
	}
	return nil
}

// ValidateBuildRequest checks the common, immutable build inputs.
func ValidateBuildRequest(req BuildRequest) error {
	if req.Ref == "" || !ValidBatchID(req.BatchID) || len(req.PRs) == 0 {
		return invalid("test ref, valid batch ID, and PRs are required")
	}
	switch req.Method {
	case Merge, Squash, Rebase, FastForward:
	default:
		return invalid("unsupported git merge method")
	}
	if _, err := BranchRef(req.Ref); err != nil {
		return err
	}
	if err := ValidateCommitIDs(req.BaseSHA); err != nil {
		return err
	}
	if err := ValidateBuildPRs(req.PRs); err != nil {
		return err
	}
	return ValidateBuildMetadata(req.PRs)
}

// ValidateLandRequest checks syntax before an invalid request can look like a
// stale lease. Both branch spellings must identify distinct refs.
func ValidateLandRequest(req LandRequest) error {
	base, err := BranchRef(req.Base)
	if err != nil {
		return err
	}
	fence, err := BranchRef(req.Fence)
	if err != nil {
		return err
	}
	if base == fence {
		return invalid("base and fence must be distinct refs")
	}
	if err := ValidateCommitIDs(req.ExpectedBaseSHA, req.ExpectedFence, req.TestedSHA); err != nil {
		return err
	}
	if req.FastForwardHeadSHA != "" {
		if err := ValidateCommitIDs(req.FastForwardHeadSHA); err != nil {
			return err
		}
	}
	return ValidateBuildPRs(req.PRs)
}

// ValidateHistoryLimit bounds enumeration independently of graph contents.
func ValidateHistoryLimit(limit, maximum int) error {
	if limit < 1 || limit > maximum {
		return invalid("invalid commit history limit")
	}
	return nil
}

// ValidatePRNumber rejects numbers that cannot identify a pull request.
func ValidatePRNumber(number int) error {
	if number <= 0 {
		return invalid("positive PR number is required")
	}
	return nil
}
