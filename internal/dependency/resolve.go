package dependency

import (
	"fmt"
	"strings"
)

// QueueKey is a supplied stored canonical binding, including base and lane.
// It is not constructed from a discovered case alias or a YAML queue name.
type QueueKey struct{ Repository, Base, Lane string }

// Binding is a recorded resolution fact. Reference must be revision-less;
// declaration pins remain on the resolved edge, not on the change identity.
type Binding struct {
	Reference Reference
	Queue     QueueKey
	Aliases   []string
}

// Registry is a complete, caller-supplied served scope snapshot. Alias bindings
// must be retained by the caller for that declaration; resolving again against
// a new snapshot is a new binding operation, not a poll-time refresh.
type Registry struct {
	Scope        Scope
	Organization string
	Repositories []string
	Bindings     []Binding
}

func (r Registry) served(repo string) bool {
	for _, candidate := range r.Repositories {
		if candidate == repo {
			return true
		}
	}
	return false
}

func (r Registry) validateRepo(repo string) error {
	u, err := repositoryURL(repo)
	if err != nil {
		return err
	}
	if r.Scope.ForgeInstance != "github:https://"+u.Host || !name(r.Organization) || !strings.HasPrefix(u.Path, "/"+r.Organization+"/") || !r.served(repo) {
		return ErrOutOfScope
	}
	return nil
}

// Resolve binds inputs only against supplied facts; no network lookup is possible.
// A foreign canonical namespace is rejected before inspecting any binding.
func Resolve(inputs []Input, registry Registry) ([]Reference, error) {
	if err := registry.Scope.Validate(); err != nil {
		return nil, err
	}
	var refs []Reference
	for _, input := range inputs {
		alias := input.Alias != ""
		if alias && input.Reference != (Reference{}) {
			return nil, ErrMalformed
		}
		if alias {
			repo, err := parseAlias(input.Alias, registry.Scope)
			if err != nil {
				return nil, err
			}
			if err := registry.validateRepo(repo); err != nil {
				return nil, err
			}
		} else {
			if input.Reference.Scope != registry.Scope {
				return nil, ErrOutOfScope
			}
			if _, err := input.Reference.Canonical(); err != nil {
				return nil, err
			}
		}
		var found *Binding
		for i := range registry.Bindings {
			b := &registry.Bindings[i]
			matches := !alias && b.Reference.ChangeID == input.Reference.ChangeID
			if alias {
				for _, value := range b.Aliases {
					if value == input.Alias {
						matches = true
					}
				}
			}
			if !matches {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("%w: ambiguous binding", ErrUnresolved)
			}
			found = b
		}
		if found == nil {
			return nil, ErrUnresolved
		}
		if found.Reference.Scope != registry.Scope {
			return nil, ErrOutOfScope
		}
		if _, err := found.Reference.Canonical(); err != nil {
			return nil, err
		}
		if found.Reference.Revision != 0 || found.Queue.Base == "" {
			return nil, ErrUnresolved
		}
		if err := registry.validateRepo(found.Queue.Repository); err != nil {
			return nil, err
		}
		if alias {
			repo, _ := parseAlias(input.Alias, registry.Scope)
			if found.Queue.Repository != repo {
				return nil, ErrUnresolved
			}
		}
		ref := found.Reference
		if !alias {
			ref.Revision = input.Reference.Revision
		}
		refs = append(refs, ref)
	}
	return Normalize(refs, registry.Scope)
}
