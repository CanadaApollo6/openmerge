// Package config loads the version 1 repository configuration from ADR 0003.
// Callers must pin repository configuration to the default branch, never a PR head.
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

	"github.com/CanadaApollo6/openmerge/internal/authority"
	cp "github.com/CanadaApollo6/openmerge/internal/changepolicy"
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

// Config is a validated version 1 repository configuration.
// Load never populates PolicyAuthority or BreakGlass while public authority
// activation is unavailable; explicit github normalizes to their zero values.
type Config struct {
	PolicyAuthority  authority.Mode            `yaml:"policy_authority,omitempty"`
	BreakGlass       *AuthorityTeam            `yaml:"break_glass,omitempty"`
	Version          int                       `yaml:"version"`
	Classification   cp.ClassificationSettings `yaml:"classification,omitempty"`
	Groups           map[string][]string       `yaml:"groups,omitempty"`
	Agents           []cp.Agent                `yaml:"agents,omitempty"`
	Policy           cp.Repository             `yaml:"-"`
	Queues           []Queue                   `yaml:"queues"`
	DeleteHeadBranch *bool                     `yaml:"delete_head_branch,omitempty"`
}

// AuthorityTeam is the dedicated break-glass team declaration reserved for activation.
type AuthorityTeam struct {
	Team string `yaml:"team"`
}

// Queue contains admission/merge conditions and serial planner settings.
type Queue struct {
	Name              string               `yaml:"name"`
	Changes           Changes              `yaml:"changes,omitempty"`
	Approvals         *cp.Approvals        `yaml:"approvals,omitempty"`
	Base              string               `yaml:"base,omitempty"`
	EnterWhen         condition.Expression `yaml:"enter_when"`
	MergeWhen         condition.Expression `yaml:"merge_when"`
	MergeMethod       queue.MergeMethod    `yaml:"merge_method"`
	CheckTimeout      time.Duration        `yaml:"check_timeout"`
	Checks            Checks               `yaml:"checks"`
	Speculation       Speculation          `yaml:"speculation"`
	Batch             Batch                `yaml:"batch"`
	baseLine          int
	basePattern       *regexp.Regexp
	basePatternSource string
}

// Changes fixes the Stage A pull-request transport requirement.
type Changes struct {
	PullRequests string `yaml:"pull_requests,omitempty"`
}

// Speculation bounds the per-queue adaptive admission window.
type Speculation struct {
	MaxDepth int `yaml:"max_depth"`
	MinDepth int `yaml:"min_depth"`
}

// Checks controls optional bounded retries for failed required checks.
// RetryFailed is deliberately an integer policy knob so the YAML contract can
// grow to a bounded retry count without changing the surrounding shape.
type Checks struct {
	RetryFailed int `yaml:"retry_failed"`
}

// Batch controls serial batch formation.
type Batch struct {
	TargetPassRate float64       `yaml:"target_pass_rate,omitempty"`
	MaxSize        int           `yaml:"max_size"`
	MaxWait        time.Duration `yaml:"max_wait"`
}

// QueueConfig maps a loaded queue to the planner. Operational defaults are a
// 1m uncertain-landing resolution wait, 1m check refresh, 3 consecutive build
// failures before pausing, and a 5s initial build backoff (doubled per failure).
func (q Queue) QueueConfig() queue.Config {
	// Zero encodes the legacy depth-1 policy without adding JSON digest fields.
	maxDepth, minDepth := q.Speculation.MaxDepth, q.Speculation.MinDepth
	if maxDepth == 1 {
		maxDepth = 0
	}
	if minDepth == 1 {
		minDepth = 0
	}
	return queue.Config{
		SpeculationMaxDepth: maxDepth, SpeculationMinDepth: minDepth,
		MergeMethod: q.MergeMethod, CheckTimeout: q.CheckTimeout,
		ChecksRetryFailed:   q.retryLimit(),
		BatchTargetPassRate: q.Batch.TargetPassRate,
		BatchMaxSize:        q.Batch.MaxSize, BatchMaxWait: q.Batch.MaxWait,
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
			return Config{}, nil, Errors{{Line: 1, Message: "version: 1 and at least one queue are required"}}
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
	fields := p.fields(root, "", configKeys...)
	cfg := Config{}
	declaration, authorityErrors := parseAuthorityDeclaration(root)
	p.errors = append(p.errors, authorityErrors...)
	if declaration.Mode == authority.OpenMerge {
		p.error(fields["policy_authority"], "policy_authority", AuthorityActivationUnavailable)
	}
	if n := fields["version"]; n == nil {
		p.error(root, "version", "required; supported versions: 1")
	} else if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&cfg.Version) != nil || cfg.Version != 1 {
		p.error(n, "version", "unsupported version %q; supported versions: 1", n.Value)
	}
	if n := fields["queues"]; n == nil {
		p.error(root, "queues", "at least one queue is required")
	} else if n.Kind != yaml.SequenceNode {
		p.error(n, "queues", "expected a list containing at least one queue")
	} else {
		if len(n.Content) == 0 {
			p.error(n, "queues", "at least one queue is required")
		}
		names := make(map[string]int)
		for i, item := range n.Content {
			q := p.queue(item, fmt.Sprintf("queues[%d]", i))
			if first, exists := names[q.Name]; exists && q.Name != "" {
				p.error(item, fmt.Sprintf("queues[%d].name", i), "duplicate queue name %q (first defined at queues[%d])", q.Name, first)
			} else {
				names[q.Name] = i
			}
			cfg.Queues = append(cfg.Queues, q)
		}
	}
	if n := fields["delete_head_branch"]; n != nil {
		var enabled bool
		if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!bool" || n.Decode(&enabled) != nil {
			p.error(n, "delete_head_branch", "expected a boolean")
		} else {
			cfg.DeleteHeadBranch = &enabled
		}
	}
	if len(p.errors) == 0 {
		loadPolicy(root, &cfg, &p)
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
	q := Queue{Speculation: Speculation{MaxDepth: 1, MinDepth: 1}, MergeMethod: queue.Squash, CheckTimeout: queue.DefaultCheckTimeout, Batch: Batch{MaxSize: 1}}
	q.baseLine = max(1, n.Line)
	fields := p.fields(n, path, queueKeys...)
	if name := fields["name"]; name == nil {
		p.error(n, path+".name", "required")
	} else {
		q.Name = p.text(name, path+".name")
		if name.ShortTag() == "!!str" && strings.TrimSpace(q.Name) == "" {
			p.error(name, path+".name", "must not be empty")
		}
	}
	if n := fields["base"]; n != nil {
		q.baseLine = max(1, n.Line)
		q.Base = p.text(n, path+".base")
		if n.ShortTag() == "!!str" {
			if strings.TrimSpace(q.Base) == "" {
				p.error(n, path+".base", "must not be empty")
			} else {
				var err error
				q.basePattern, err = condition.CompileGlob(q.Base)
				q.basePatternSource = q.Base
				if err != nil {
					p.error(n, path+".base", "invalid glob %q: %v", q.Base, err)
				}
			}
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
		case queue.Merge, queue.Squash, queue.Rebase, queue.FastForward:
			q.MergeMethod = queue.MergeMethod(method)
		default:
			p.error(n, path+".merge_method", "expected merge, squash, rebase, or fast-forward, got %q", method)
		}
	}
	if n := fields["check_timeout"]; n != nil {
		q.CheckTimeout = p.duration(n, path+".check_timeout")
	}
	if n := fields["checks"]; n != nil {
		fields := p.fields(n, path+".checks", checksKeys...)
		if n := fields["retry_failed"]; n != nil {
			if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&q.Checks.RetryFailed) != nil || q.Checks.RetryFailed < 0 || q.Checks.RetryFailed > 1 {
				p.error(n, path+".checks.retry_failed", "expected an integer 0 or 1")
			}
		}
	}
	if n := fields["speculation"]; n != nil {
		fields := p.fields(n, path+".speculation", speculationKeys...)
		for _, field := range []struct {
			name  string
			value *int
		}{{"max_depth", &q.Speculation.MaxDepth}, {"min_depth", &q.Speculation.MinDepth}} {
			if v := fields[field.name]; v != nil {
				if v.Kind != yaml.ScalarNode || v.ShortTag() != "!!int" || v.Decode(field.value) != nil || *field.value < 1 || *field.value > 32 {
					p.error(v, path+".speculation."+field.name, "expected an integer from 1 to 32")
				}
			}
		}
		if q.Speculation.MinDepth > q.Speculation.MaxDepth {
			p.error(n, path+".speculation", "min_depth must not exceed max_depth")
		}
	}
	if n := fields["batch"]; n != nil {
		fields := p.fields(n, path+".batch", batchKeys...)
		if n := fields["max_size"]; n != nil {
			if n.Kind != yaml.ScalarNode || n.ShortTag() != "!!int" || n.Decode(&q.Batch.MaxSize) != nil || q.Batch.MaxSize < 1 || q.Batch.MaxSize > 128 {
				p.error(n, path+".batch.max_size", "expected an integer from 1 to 128")
			}
		}
		if n := fields["target_pass_rate"]; n != nil {
			if n.Kind != yaml.ScalarNode || (n.ShortTag() != "!!float" && n.ShortTag() != "!!int") || n.Decode(&q.Batch.TargetPassRate) != nil || q.Batch.TargetPassRate == 0 || !queue.ValidTargetPassRate(q.Batch.TargetPassRate) {
				p.error(n, path+".batch.target_pass_rate", "expected a number strictly between 0 and 1")
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

func (q Queue) retryLimit() int {
	if q.Speculation.MaxDepth > 1 {
		return 1
	}
	return q.Checks.RetryFailed
}
