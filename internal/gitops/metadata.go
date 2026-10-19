package gitops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/ascii"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

var openmergeMarker = regexp.MustCompile(`<!--\s*openmerge:[\s\S]*?-->`)

// Reserved keys cannot acquire authority from contributor prose or history.
func stripMetadataLines(message string, keys ...string) string {
	var kept []string
	remove := false
	for line := range strings.SplitSeq(message, "\n") {
		if remove && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		remove = false
		for _, reserved := range keys {
			remove = remove || ok && strings.EqualFold(strings.TrimSpace(key), reserved)
			if reserved == "OpenMerge-*" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "openmerge-") {
				remove = true
			}
			if reserved == "OpenMerge-Batch" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "openmerge-batch") {
				remove = true
			}
		}
		if !remove {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// A patch divider hides all subsequent trailers from Git's default parser.
func rewritePatchDividers(message string) string {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "---") {
			lines[i] = "- - -" + line[3:]
		}
	}
	return strings.Join(lines, "\n")
}

func validIdentity(id Identity) bool {
	return id.Name != "" && id.Email != "" && !strings.ContainsAny(id.Name+id.Email, "<>\x00\r\n")
}

func (s *session) metadata(ctx context.Context, repo forge.RepoRef, pin forge.BuildPR) (forge.PullRequest, Identity, error) {
	pr := forge.PullRequest{Number: pin.Number, HeadSHA: pin.HeadSHA, Title: fmt.Sprintf("PR %d", pin.Number), HeadBranch: pin.HeadSHA}
	author := s.r.cfg.Author
	if s.r.cfg.PullRequest != nil {
		var err error
		pr, err = s.r.cfg.PullRequest(ctx, repo, pin.Number)
		if err != nil {
			return pr, author, err
		}
		if pr.Number != pin.Number || pr.HeadSHA != pin.HeadSHA {
			return pr, author, fmt.Errorf("PR %d metadata no longer matches its pinned head", pin.Number)
		}
		author = Identity{Name: pr.Author, Email: pr.AuthorEmail}
	}
	if !validIdentity(author) || strings.TrimSpace(pr.Title) == "" || strings.ContainsAny(pr.Title+pr.HeadBranch, "\x00\r\n") {
		return pr, author, errors.New("invalid PR commit metadata")
	}
	return pr, author, nil
}

func (s *session) original(ctx context.Context, sha string) (Identity, string, string, error) {
	out, err := s.command(ctx, "", "show", "-s", "--format=%an%x00%ae%x00%aI%x00%B", sha)
	if err != nil {
		return Identity{}, "", "", err
	}
	fields := strings.SplitN(out, "\x00", 4)
	if len(fields) != 4 {
		return Identity{}, "", "", errors.New("invalid original commit metadata")
	}
	return Identity{Name: fields[0], Email: fields[1]}, fields[2], fields[3], nil
}

func (s *session) commitAs(ctx context.Context, tree, message string, author Identity, date string, parents ...string) (string, error) {
	authored := *s
	authored.author, authored.authorDate = author, date
	return authored.commit(ctx, tree, message, parents...)
}

// Git parses only the trailer block, so prose mentioning a sign-off is not
// promoted to a trailer. It also handles continuation lines and key casing.
func (s *session) trailers(ctx context.Context, message string) ([]string, error) {
	message = rewritePatchDividers(stripMetadataLines(message, "OpenMerge-Batch"))
	out, err := s.command(ctx, message+"\n", "interpret-trailers", "--parse")
	if err != nil {
		return nil, err
	}
	var trailers []string
	for line := range strings.SplitSeq(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(key) {
		case "signed-off-by":
			trailers = append(trailers, "Signed-off-by: "+strings.TrimSpace(value))
		case "co-authored-by":
			trailers = append(trailers, "Co-authored-by: "+strings.TrimSpace(value))
		}
	}
	return trailers, nil
}

func (s *session) addTrailers(ctx context.Context, message string, trailers ...string) (string, error) {
	args := []string{"interpret-trailers", "--no-divider", "--if-exists=addIfDifferent", "--if-missing=add"}
	for _, trailer := range trailers {
		args = append(args, "--trailer", trailer)
	}
	out, err := s.command(ctx, rewritePatchDividers(message)+"\n", args...)
	if err != nil {
		return "", err
	}
	if len(trailers) > 0 && strings.HasPrefix(trailers[len(trailers)-1], "OpenMerge-Batch: ") {
		batch := trailers[len(trailers)-1]
		before, after, ok := strings.Cut(out+"\n", "\n"+batch+"\n")
		if !ok {
			return "", errors.New("git did not append the batch trailer")
		}
		// Git inserts trailers before trailing comments. Keep the comments in
		// the block without blank lines, so attribution and batch stay visible.
		for line := range strings.SplitSeq(after, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !strings.HasPrefix(line, "#") {
				return "", errors.New("unexpected content after the batch trailer")
			}
			before += "\n" + line
		}
		out = before + "\n" + batch
	}
	return out, nil
}

func (s *session) prMessage(ctx context.Context, repo forge.RepoRef, req forge.BuildRequest, pr forge.PullRequest, author Identity, metadata *forge.ChangeMetadata, last bool) (string, error) {
	reserved := "OpenMerge-Batch"
	if metadata != nil {
		reserved = "OpenMerge-*"
	}
	title := strings.TrimSpace(stripMetadataLines(pr.Title, reserved))
	if title == "" {
		title = fmt.Sprintf("PR %d", pr.Number)
	}
	message := fmt.Sprintf("%s (#%d)", title, pr.Number)
	body := strings.TrimSpace(stripMetadataLines(openmergeMarker.ReplaceAllString(pr.Body, ""), "Signed-off-by", "Co-authored-by", reserved))
	if req.Method == forge.Merge {
		message = fmt.Sprintf("Merge pull request #%d from %s", pr.Number, pr.HeadBranch)
		body = title
	}
	if body != "" {
		message += "\n\n" + body
	}
	message = rewritePatchDividers(message)
	originals, err := s.command(ctx, "", "rev-list", "--reverse", "--topo-order", pr.HeadSHA, "^"+req.BaseSHA)
	if err != nil {
		return "", err
	}
	var trailers []string
	seen := make(map[string]bool)
	coauthors := map[string]bool{strings.ToLower(author.Email): true}
	if s.r.cfg.CommitAuthors != nil {
		linked, err := s.r.cfg.CommitAuthors(ctx, repo, pr.Number)
		if err != nil {
			return "", err
		}
		own := make(map[string]bool)
		// Nil metadata is the legacy queue path: preserve main's Unicode
		// fallback exactly. Approval builds use ASCII identity comparison.
		equal := strings.EqualFold
		if metadata != nil {
			equal = ascii.EqualFold
		}
		for _, commit := range linked {
			matches := commit.Login != "" && equal(commit.Login, pr.Author)
			if commit.ID > 0 && pr.AuthorID > 0 {
				matches = commit.ID == pr.AuthorID
			}
			own[commit.SHA] = matches
		}
		// Collect every own email first, so trailers in earlier commits cannot
		// reintroduce another email belonging to the primary author.
		for _, sha := range strings.Fields(originals) {
			if own[sha] {
				id, _, _, err := s.original(ctx, sha)
				if err != nil {
					return "", err
				}
				coauthors[strings.ToLower(id.Email)] = true
			}
		}
	}
	add := func(trailer string) {
		key, value, _ := strings.Cut(trailer, ": ")
		if key == "Co-authored-by" {
			identity := strings.ToLower(value)
			if i := strings.LastIndex(value, "<"); i >= 0 && strings.HasSuffix(value, ">") {
				identity = strings.ToLower(strings.TrimSpace(value[i+1 : len(value)-1]))
			}
			if coauthors[identity] {
				return
			}
			coauthors[identity] = true
		}
		if !seen[strings.ToLower(trailer)] {
			seen[strings.ToLower(trailer)] = true
			trailers = append(trailers, trailer)
		}
	}
	for _, sha := range strings.Fields(originals) {
		other, _, original, err := s.original(ctx, sha)
		if err != nil {
			return "", err
		}
		add(fmt.Sprintf("Co-authored-by: %s <%s>", other.Name, other.Email))
		retained, err := s.trailers(ctx, original)
		if err != nil {
			return "", err
		}
		for _, trailer := range retained {
			add(trailer)
		}
	}
	changeTrailers, err := metadata.Trailers()
	if err != nil {
		return "", err
	}
	trailers = append(trailers, changeTrailers...)
	if last {
		trailers = append(trailers, "OpenMerge-Batch: "+req.BatchID)
	}
	if len(trailers) == 0 {
		return message, nil
	}
	return strings.TrimSpace(message) + "\n\n" + strings.Join(trailers, "\n"), nil
}
