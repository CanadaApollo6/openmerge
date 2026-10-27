package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/provenance"
)

var provenanceParserArgs = []string{"-c", "trailer.separators=:", "-c", "core.commentChar=#", "-c", "core.commentString=#"}

// TrailerParser owns a template-free context. Close removes its private files.
// It is independent of source-repository config and never invokes a transport.
type TrailerParser struct {
	s      *session
	policy provenance.ParserPolicy
}

// NewTrailerParser creates private local storage beneath scratch. Its caller
// supplies the evaluation deadline; Parse also enforces a process ceiling.
func NewTrailerParser(ctx context.Context, scratch string) (*TrailerParser, error) {
	ctx, cancel := context.WithTimeout(ctx, provenance.DefaultInventoryLimits().Duration)
	defer cancel()
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratch, "provenance-parser-*")
	if err != nil {
		return nil, err
	}
	s := &session{r: &Repo{git: git, cfg: Config{Now: time.Now}}, diffRead: true}
	parser := &TrailerParser{s: s}
	// Always select a directory explicitly, including during init/version, to
	// exclude the process working directory's repository configuration.
	s.dir = filepath.Join(dir, "parser.git")
	// init needs an existing empty GIT_DIR on Git versions that inspect it first.
	if err = os.Mkdir(s.dir, 0o700); err == nil {
		_, err = s.provenanceCommand(ctx, "", 4096, "init", "--bare", "--template=", s.dir)
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	version, err := s.provenanceCommand(ctx, "", 4096, "version")
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	parser.policy = provenance.NewParserPolicy(strings.TrimSpace(version))
	return parser, nil
}

// Close releases the parser's private directory.
func (p *TrailerParser) Close() error { return os.RemoveAll(filepath.Dir(p.s.dir)) }

// Policy returns the actual Git version and fixed adapter semantics.
func (p *TrailerParser) Policy() provenance.ParserPolicy { return p.policy }

// Parse is the production producer of GitTrailerOutput. Original bytes pass
// unchanged to Git. Errors return no usable output, including embedded NULs.
func (p *TrailerParser) Parse(ctx context.Context, message string) (provenance.GitTrailerOutput, error) {
	if strings.ContainsRune(message, 0) {
		return "", errors.New("NUL in original message")
	}
	if len(message) > provenance.DefaultInventoryLimits().CommitBytes {
		return "", errDiffOutputLimit
	}
	ctx, cancel := context.WithTimeout(ctx, provenance.DefaultInventoryLimits().Duration)
	defer cancel()
	args := append(append([]string{}, provenanceParserArgs...), "interpret-trailers", "--parse", "--no-divider")
	out, err := p.s.provenanceCommand(ctx, message, 2*provenance.DefaultInventoryLimits().CommitBytes, args...)
	if err != nil {
		return "", err
	}
	return provenance.GitTrailerOutput(out), nil
}

// Bound stdout and stderr while reading. Never expose partial output or raw
// diagnostics (which can contain contributor-controlled sensitive content).
func (s *session) provenanceCommand(ctx context.Context, input string, limit int, args ...string) (string, error) {
	out := &limitedDiffBuffer{limit: limit}
	diagnostic := &limitedDiffBuffer{limit: 8192}
	err := s.runTo(ctx, input, out, diagnostic, args...)
	if out.overflow || diagnostic.overflow {
		return "", errDiffOutputLimit
	}
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// provenanceLimitError keeps the resource name when graph acquisition hits a
// lower-level raw-object ceiling.
type provenanceLimitError struct{ resource string }

func (e *provenanceLimitError) Error() string { return "provenance limit: " + e.resource }
func (e *provenanceLimitError) Unwrap() error { return errDiffOutputLimit }
