package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Auth supplies HTTP credentials to Git's askpass helper through the child
// process environment. For tokens, use the forge's token username and password.
type Auth struct{ Username, Password string }

// ErrAuth identifies rejected or unavailable Git transport credentials.
var ErrAuth = errors.New("git authentication failed")

const askpassScript = `#!/bin/sh
case "$1" in
  *Username*) printf '%s\n' "$OPENMERGE_GIT_USERNAME" ;;
  *Password*) printf '%s\n' "$OPENMERGE_GIT_PASSWORD" ;;
  *) exit 1 ;;
esac
`

var urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s]*@`)

type session struct {
	r           *Repo
	dir, remote string
	auth        Auth
	author      Identity
	authorDate  string
	diffRead    bool
}

func (s *session) scrub(message string) string {
	message = urlUserinfo.ReplaceAllString(message, "${1}[redacted]@")
	for _, secret := range []string{s.auth.Password, s.auth.Username} {
		if secret != "" {
			for _, encoded := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
				message = strings.ReplaceAll(message, encoded, "[redacted]")
			}
		}
	}
	return message
}

// Never inherit tracing, credentials, injected config, alternate object stores,
// or repository selection from the process running OpenMerge.
func gitEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") && !strings.HasPrefix(key, "OPENMERGE_GIT_") && key != "LC_ALL" && key != "LANG" {
			env = append(env, entry)
		}
	}
	return append(env, "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func (s *session) run(ctx context.Context, input string, args ...string) (stdout, stderr string, err error) {
	var out, diagnostic bytes.Buffer
	err = s.runTo(ctx, input, &out, &diagnostic, args...)
	return out.String(), diagnostic.String(), err
}

func (s *session) newCommand(ctx context.Context, args ...string) *exec.Cmd {
	argv := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-c", "commit.gpgSign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	if s.diffRead {
		argv = append(argv, "-c", "core.commitGraph=false")
	}
	if s.dir != "" {
		argv = append(argv, "--git-dir="+s.dir)
	}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, s.r.git, argv...)
	cmd.Env = append(gitEnv(), "GIT_ASKPASS="+s.r.askpass,
		"OPENMERGE_GIT_USERNAME="+s.auth.Username, "OPENMERGE_GIT_PASSWORD="+s.auth.Password)
	if s.diffRead {
		cmd.Env = append(cmd.Env, "GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE="+os.DevNull, "GIT_NO_LAZY_FETCH=1")
		// Git 2.38 predates GIT_NO_LAZY_FETCH. Permit transport only for our
		// explicit fetch, so older supported Git cannot hydrate during a read.
		if len(args) > 0 && args[0] != "fetch" {
			cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=")
		}
		// Inspect the real shallow file separately. Ancestry must always follow
		// raw parents, even if a boundary appears and disappears during Git.
		if len(args) > 0 && (args[0] == "merge-base" || args[0] == "rev-list") {
			cmd.Env = append(cmd.Env, "GIT_SHALLOW_FILE="+os.DevNull)
		}
	}
	cmd.Env = append(cmd.Env, s.r.identityEnv()...)
	if s.author != (Identity{}) {
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+s.author.Name, "GIT_AUTHOR_EMAIL="+s.author.Email)
	}
	if s.authorDate != "" {
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+s.authorDate)
	}
	return cmd
}

func (s *session) runTo(ctx context.Context, input string, out, diagnostic io.Writer, args ...string) error {
	cmd := s.newCommand(ctx, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout, cmd.Stderr = out, diagnostic
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return err
}

func (s *session) command(ctx context.Context, input string, args ...string) (string, error) {
	out, diagnostic, err := s.run(ctx, input, args...)
	if err != nil {
		if strings.TrimSpace(diagnostic) == "" {
			diagnostic = out
		}
		return "", s.failure(args[0], diagnostic, err)
	}
	return strings.TrimSpace(out), nil
}

func (s *session) failure(operation, diagnostic string, err error) error {
	// exec errors do not contain argv. Remote diagnostics may contain URLs or
	// echoed credentials, so sanitize them before they leave this package.
	if authFailure(diagnostic) {
		err = errors.Join(ErrAuth, err)
	}
	diagnostic = s.scrub(strings.TrimSpace(diagnostic))
	if diagnostic == "" {
		return fmt.Errorf("git %s: %w", operation, err)
	}
	return fmt.Errorf("git %s: %s: %w", operation, diagnostic, err)
}

func authFailure(diagnostic string) bool {
	diagnostic = strings.ToLower(diagnostic)
	for _, marker := range []string{"authentication failed", "http basic: access denied", "could not read username",
		"could not read password", "permission denied (", "returned error: 401", "returned error: 403"} {
		if strings.Contains(diagnostic, marker) {
			return true
		}
	}
	return false
}

func exitCode(err error, code int) bool {
	var status *exec.ExitError
	return errors.As(err, &status) && status.ExitCode() == code
}

func cleanRemote(remote string, auth Auth) (string, Auth, error) {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\r\n") {
		return "", auth, errors.New("invalid git remote")
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", auth, errors.New("invalid git remote URL")
		}
		if u.User != nil {
			if auth.Username == "" {
				auth.Username = u.User.Username()
			}
			if auth.Password == "" {
				auth.Password, _ = u.User.Password()
			}
			u.User = nil
		}
		remote = u.String()
	}
	return remote, auth, nil
}
