package forge

import (
	"errors"
	"github.com/CanadaApollo6/openmerge/internal/authority"
)

// ValidateAuthorityAcquisition checks raw typed inventory, before interpretation
// adds coverage. Missing/partial reads cannot establish an activation binding.
func ValidateAuthorityAcquisition(o authority.Observation) error {
	if o.Validate() != nil || o.Inventory != authority.Visible {
		return errors.New("authority governing protection evidence incomplete; obtain a complete seeing read")
	}
	for _, p := range o.Protections {
		if p.TargetsVisibility != authority.Visible || p.RulesVisibility != authority.Visible || p.BypassVisibility != authority.Visible {
			return errors.New("authority governing protection visibility incomplete; obtain a complete seeing read")
		}
	}
	return nil
}
