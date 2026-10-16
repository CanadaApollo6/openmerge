// Package config loads the version 1 repository configuration from ADR 0003.
// Callers must read repository configuration from the base branch, never a PR head.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/CanadaApollo6/openmerge/internal/config/condition"
	"github.com/CanadaApollo6/openmerge/internal/queue"
	"go.yaml.in/yaml/v3"
)

// ErrNotFound means none of the four supported repository paths exists.
var ErrNotFound = errors.New("no OpenMerge configuration found")

// Find reads all supported paths in order, rejecting ambiguous configurations.
// A read error is never treated as an absent file.
func Find(read func(path string) ([]byte, bool, error)) (path string, data []byte, err error) {
	var found []string
	paths := []string{
		".openmerge.yml", ".openmerge.yaml", ".github/openmerge.yml", ".github/openmerge.yaml",
	}
	for _, candidate := range paths {
		contents, exists, readErr := read(candidate)
		if readErr != nil {
			return "", nil, fmt.Errorf("read %s: %w", candidate, readErr)
		}
		if exists {
			found = append(found, candidate)
			path, data = candidate, contents
		}
	}
	switch len(found) {
	case 0:
		return "", nil, fmt.Errorf("%w; searched: %s", ErrNotFound, strings.Join(paths, ", "))
	case 1:
		return path, data, nil
	default:
		return "", nil, fmt.Errorf("multiple OpenMerge configuration files found: %s", strings.Join(found, ", "))
	}
}

// Config is a validated version 1 configuration. Phase 1 has exactly one queue.
type Config struct {
	Version int
	Queues  []Queue
}

// Queue contains admission/merge conditions and serial planner settings.
type Queue struct {
	Name         string
	EnterWhen    condition.Expression
	MergeWhen    condition.Expression
	MergeMethod  queue.MergeMethod
	CheckTimeout time.Duration
	Batch        Batch
}

// Batch controls serial batch formation.
type Batch struct {
	MaxSize int
	MaxWait time.Duration
}

// QueueConfig maps a loaded queue to the planner. Operational defaults are a
// 1m uncertain-landing resolution wait, 1m check refresh, 3 consecutive build
// failures before pausing, and a 5s initial build backoff (doubled per failure).
func (q Queue) QueueConfig() queue.Config {
	return queue.Config{
		MergeMethod: q.MergeMethod, CheckTimeout: q.CheckTimeout,
		BatchMaxSize: q.Batch.MaxSize, BatchMaxWait: q.Batch.MaxWait,
		ResolveAfter: time.Minute, RefreshInterval: time.Minute,
		BuildRetries: 3, BuildBackoff: 5 * time.Second,
	}
}

// Diagnostic locates a validation error or warning. Path uses schema field names
// and zero-based sequence indices; Line is one-based, including for missing keys.
type Diagnostic struct {
	Line    int
	Path    string
	Message string
}

func (d Diagnostic) Error() string {
	if d.Path == "" {
		return fmt.Sprintf("line %d: %s", d.Line, d.Message)
	}
	return fmt.Sprintf("line %d: %s: %s", d.Line, d.Path, d.Message)
}

// Errors contains all independent schema and condition validation errors.
type Errors []Diagnostic

func (errs Errors) Error() string {
	messages := make([]string, len(errs))
	for i, err := range errs {
		messages[i] = err.Error()
	}
	return strings.Join(messages, "\n")
}

// Load parses exactly one YAML document, validates it, and compiles conditions.
// Errors are returned as Errors, including YAML syntax errors. Invalid input
// returns no usable configuration.
func Load(data []byte) (Config, []Diagnostic, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil, Errors{{Line: 1, Message: "version: 1 and exactly one queue are required"}}
		}
		return Config{}, nil, syntaxError(err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, nil, syntaxError(err)
		}
		return Config{}, nil, Errors{{Line: max(1, extra.Line), Message: "expected exactly one YAML document"}}
	}
	budget := 10000
	root, err := expand(doc.Content[0], make(map[*yaml.Node]bool), 0, &budget)
	if err != nil {
		return Config{}, nil, Errors{*err}
	}
	p := parser{}
	fields := p.fields(root, "", "version", "queues")
	cfg := Config{}
	if n := fields["version"]; n == nil {
		p.error(root, "version", "required; supported versions: 1")
	} else if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&cfg.Version) != nil || cfg.Version != 1 {
		p.error(n, "version", "unsupported version %q; supported versions: 1", n.Value)
	}
	if n := fields["queues"]; n == nil {
		p.error(root, "queues", "exactly one queue is required")
	} else if n.Kind != yaml.SequenceNode {
		p.error(n, "queues", "expected a list containing exactly one queue")
	} else {
		if len(n.Content) == 0 {
			p.error(n, "queues", "exactly one queue is required")
		} else if len(n.Content) > 1 {
			p.error(n, "queues", "multiple queues arrive in a later release; Phase 1 supports exactly one queue")
		}
		for i, item := range n.Content {
			cfg.Queues = append(cfg.Queues, p.queue(item, fmt.Sprintf("queues[%d]", i)))
		}
	}
	sortDiagnostics(p.errors)
	sortDiagnostics(p.warnings)
	if len(p.errors) != 0 {
		return Config{}, nil, p.errors
	}
	return cfg, p.warnings, nil
}

func sortDiagnostics(diagnostics []Diagnostic) {
	slices.SortStableFunc(diagnostics, func(a, b Diagnostic) int { return a.Line - b.Line })
}

var yamlLine = regexp.MustCompile(`\bline (\d+)`)

func syntaxError(err error) Errors {
	line := 1
	if match := yamlLine.FindStringSubmatch(err.Error()); match != nil {
		line, _ = strconv.Atoi(match[1])
	}
	return Errors{{Line: line, Message: err.Error()}}
}

type parser struct {
	errors   Errors
	warnings []Diagnostic
}

func (p *parser) error(n *yaml.Node, path, format string, args ...any) {
	p.errors = append(p.errors, Diagnostic{Line: max(1, n.Line), Path: path, Message: fmt.Sprintf(format, args...)})
}

func (p *parser) queue(n *yaml.Node, path string) Queue {
	before := len(p.errors)
	q := Queue{MergeMethod: queue.Squash, CheckTimeout: time.Hour, Batch: Batch{MaxSize: 1}}
	fields := p.fields(n, path, "name", "enter_when", "merge_when", "merge_method", "check_timeout", "batch")
	if name := fields["name"]; name == nil {
		p.error(n, path+".name", "required")
	} else {
		q.Name = p.text(name, path+".name")
		if name.ShortTag() == "!!str" && strings.TrimSpace(q.Name) == "" {
			p.error(name, path+".name", "must not be empty")
		}
	}
	if n := fields["enter_when"]; n != nil {
		q.EnterWhen = condition.All(p.conditionList(n, path+".enter_when")...)
	}
	if n := fields["merge_when"]; n != nil {
		q.MergeWhen = condition.All(p.conditionList(n, path+".merge_when")...)
	}
	if len(p.errors) == before {
		required := q.MergeWhen.RequiredChecks()
		missing, _ := q.MergeWhen.Evaluate(condition.CheckResults(nil, nil, nil))
		failed, _ := q.MergeWhen.Evaluate(condition.Snapshot{
			"check-success": condition.Strings(),
			"check-failure": condition.Strings(required...),
			"check-pending": condition.Strings(),
		})
		if len(required) == 0 || missing || failed {
			location := fields["merge_when"]
			if location == nil {
				location = n
			}
			p.error(location, path+".merge_when", "queue %q merge_when must require a positive named check-success equality in its conjunction and reject missing or failed checks (for example merge_when: [check-success = ci]); checks under or/not and counts do not qualify", q.Name)
		}
	}
	if n := fields["merge_method"]; n != nil {
		method := p.text(n, path+".merge_method")
		switch queue.MergeMethod(method) {
		case queue.Merge, queue.Squash, queue.Rebase:
			q.MergeMethod = queue.MergeMethod(method)
		default:
			p.error(n, path+".merge_method", "expected merge, squash, or rebase, got %q", method)
		}
	}
	if n := fields["check_timeout"]; n != nil {
		q.CheckTimeout = p.duration(n, path+".check_timeout")
	}
	if n := fields["batch"]; n != nil {
		fields := p.fields(n, path+".batch", "max_size", "max_wait")
		if n := fields["max_size"]; n != nil {
			if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&q.Batch.MaxSize) != nil || q.Batch.MaxSize < 1 || q.Batch.MaxSize > 128 {
				p.error(n, path+".batch.max_size", "expected an integer from 1 to 128")
			}
		}
		if n := fields["max_wait"]; n != nil {
			q.Batch.MaxWait = p.duration(n, path+".batch.max_wait")
		}
	}
	return q
}

func (p *parser) text(n *yaml.Node, path string) string {
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		p.error(n, path, "expected a string")
		return ""
	}
	return n.Value
}

func (p *parser) duration(n *yaml.Node, path string) time.Duration {
	// Unquoted YAML 0 is a convenient spelling of the disabled duration.
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!int" && n.Value == "0" {
		return 0
	}
	if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!str" {
		p.error(n, path, "expected a nonnegative Go duration string (0 disables)")
		return 0
	}
	d, err := time.ParseDuration(n.Value)
	if err != nil || d < 0 {
		p.error(n, path, "expected a nonnegative Go duration string, got %q", n.Value)
		return 0
	}
	return d
}
