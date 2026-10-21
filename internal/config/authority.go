package config

import (
	"fmt"
	"reflect"

	"github.com/CanadaApollo6/openmerge/internal/authority"
	"github.com/CanadaApollo6/openmerge/internal/classification"
	"github.com/CanadaApollo6/openmerge/internal/forge"
)

// AuthorityActivationUnavailable is the public fail-closed barrier until PR 9c.
const AuthorityActivationUnavailable = "openmerge authority activation is unavailable: the complete ADR 0008 authority gate is not enabled"

// ValidateGoverning checks a candidate against immutable default-branch evidence.
// It grants no runtime activation and has no import-accept bypass. Actor binding
// and current protection observations remain separate activation prerequisites.
func (c AuthorityCandidate) ValidateGoverning(defaultBranch, commit string, g forge.GoverningFiles, resolve classification.OwnerResolver) error {
	if err := c.Authority.Validate(); err != nil {
		return err
	}
	if c.Authority.Mode.Normalized() != authority.OpenMerge {
		return nil
	}
	if c.Authority.BreakGlass.Team == "" || c.Authority.BreakGlass.RepositoryAdmin {
		return fmt.Errorf("break_glass requires a dedicated team")
	}
	if _, err := authority.CompileRefCoverage(defaultBranch, c.BaseSelectors()); err != nil {
		return err
	}
	if _, errs := c.Config.ResolveBranch(defaultBranch, defaultBranch); len(errs) != 0 {
		return errs
	}
	if err := classification.ValidateAuthorityOwners(commit, g, resolve); err != nil {
		return err
	}
	for _, file := range g.Config {
		if !file.Present {
			continue
		}
		governing, _, err := LoadAuthorityCandidate([]byte(file.Content))
		if err != nil {
			return fmt.Errorf("invalid governing authority config: %w", err)
		}
		if !reflect.DeepEqual(c, governing) {
			return fmt.Errorf("authority candidate does not match governing config at %s", commit)
		}
	}
	return nil
}
