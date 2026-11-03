package cidoctor

import (
	"errors"
	"slices"
	"strings"

	"github.com/CanadaApollo6/openmerge/internal/config"
	"github.com/CanadaApollo6/openmerge/internal/forge"
	"go.yaml.in/yaml/v3"
)

type workflowJob struct {
	Name     string    `yaml:"name"`
	Uses     string    `yaml:"uses"`
	Needs    yaml.Node `yaml:"needs"`
	Strategy struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
}

// workflowGraph accepts literal job names and static Cartesian matrices only.
// Expressions, reusable workflows and include/exclude matrices need a resolver;
// returning nil keeps dependency-based advice silent instead of guessing.
func workflowGraph(r Run, data []byte) *Graph {
	var doc struct {
		Jobs map[string]workflowJob `yaml:"jobs"`
	}
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Jobs) == 0 {
		return nil
	}
	names := map[string][]string{}
	for id, job := range doc.Jobs {
		name := job.Name
		if name == "" {
			name = id
		}
		if job.Uses != "" || strings.Contains(name, "${{") {
			return nil
		}
		names[id] = []string{name}
		matrix := job.Strategy.Matrix
		if matrix.Kind == 0 {
			continue
		}
		if matrix.Kind != yaml.MappingNode || job.Name != "" {
			return nil
		}
		combinations := []string{""}
		for i := 0; i < len(matrix.Content); i += 2 {
			key, values := matrix.Content[i], matrix.Content[i+1]
			if key.Value == "include" || key.Value == "exclude" || values.Kind != yaml.SequenceNode || len(values.Content) == 0 {
				return nil
			}
			var next []string
			for _, prefix := range combinations {
				for _, value := range values.Content {
					if value.Kind != yaml.ScalarNode || strings.Contains(value.Value, "${{") {
						return nil
					}
					v := value.Value
					if prefix != "" {
						v = prefix + ", " + v
					}
					next = append(next, v)
					if len(next) > 256 {
						return nil
					}
				}
			}
			combinations = next
		}
		names[id] = nil
		for _, values := range combinations {
			names[id] = append(names[id], name+" ("+values+")")
		}
	}
	graph := &Graph{HeadSHA: r.HeadSHA, Source: r.Path + " at " + r.HeadSHA, Needs: map[string][]string{}}
	for id, job := range doc.Jobs {
		var deps []string
		switch job.Needs.Kind {
		case 0:
		case yaml.ScalarNode:
			deps = []string{job.Needs.Value}
		case yaml.SequenceNode:
			for _, n := range job.Needs.Content {
				if n.Kind != yaml.ScalarNode {
					return nil
				}
				deps = append(deps, n.Value)
			}
		default:
			return nil
		}
		expanded := []string{}
		for _, dep := range deps {
			if _, ok := names[dep]; !ok {
				return nil
			}
			expanded = append(expanded, names[dep]...)
		}
		slices.Sort(expanded)
		for _, name := range names[id] {
			if _, exists := graph.Needs[name]; exists {
				return nil
			}
			graph.Needs[name] = slices.Clone(expanded)
		}
	}
	return graph
}

func requiredChecks(read func(string, string) ([]byte, error), sha, base, lane, defaultBranch string) ([]string, error) {
	// Configuration resolves policies by base branch; it does not define lanes.
	if lane != "" {
		return nil, nil
	}
	_, data, err := config.Find(func(path string) ([]byte, bool, error) {
		data, err := read(sha, path)
		if errors.Is(err, forge.ErrFileNotFound) {
			return nil, false, nil
		}
		return data, err == nil, err
	})
	if errors.Is(err, config.ErrNotFound) || errors.Is(err, config.ErrMultipleFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg, _, err := config.Load(data)
	if err != nil {
		return nil, nil
	}
	q, diagnostics := cfg.ResolveBranch(base, defaultBranch)
	if len(diagnostics) != 0 {
		return nil, nil
	}
	return append([]string{}, q.MergeWhen.RequiredChecks()...), nil
}
