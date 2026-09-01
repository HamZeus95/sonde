package model

import "fmt"

// GroupExistsSpec asks whether the group a runbook tells you to be in still
// exists in the identity provider. A renamed group is invisible until the
// person who needed it is paged.
type GroupExistsSpec struct {
	// Provider names the IdP: keycloak today, others as connectors land.
	Provider string `json:"provider"`
	Group    string `json:"group"`
	// Realm optionally overrides the realm; when empty the runbook's
	// environment names it, mirroring how cluster works for kubernetes checks.
	Realm string `json:"realm,omitempty"`
}

// Validate implements Spec.
func (s *GroupExistsSpec) Validate() error {
	if s.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if s.Group == "" {
		return fmt.Errorf("group is required")
	}
	return nil
}
