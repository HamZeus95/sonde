package model

import "fmt"

// ParsesSpec asks whether a documented command still makes sense: whether it
// parses, and whether the objects it names exist.
//
// The command is never executed. It is parsed into a target and checked the way
// any other assertion is; a runbook step that says `kubectl delete` is still
// only ever read.
type ParsesSpec struct {
	Command string `json:"command"`
	// Cluster and Namespace supply context the command line omits.
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

// Validate implements Spec.
func (s *ParsesSpec) Validate() error {
	if s.Command == "" {
		return fmt.Errorf("command is required")
	}
	return nil
}
