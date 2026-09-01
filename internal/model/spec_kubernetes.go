package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResourceRef is the `type/name` form a runbook uses to name a Kubernetes
// object, mirroring how the same object appears in the kubectl command printed
// beside it: deployment/payments-api.
type ResourceRef struct {
	Type string
	Name string
}

// ParseResourceRef splits a `type/name` reference. It is pure: it neither
// resolves short names against a live discovery API nor contacts a cluster.
func ParseResourceRef(s string) (ResourceRef, error) {
	typ, name, found := strings.Cut(strings.TrimSpace(s), "/")
	if !found {
		return ResourceRef{}, fmt.Errorf("resource %q must be of the form type/name, such as deployment/payments-api", s)
	}
	typ, name = strings.TrimSpace(typ), strings.TrimSpace(name)
	if typ == "" || name == "" {
		return ResourceRef{}, fmt.Errorf("resource %q must be of the form type/name, such as deployment/payments-api", s)
	}
	if strings.Contains(name, "/") {
		return ResourceRef{}, fmt.Errorf("resource %q has more than one /", s)
	}
	return ResourceRef{Type: typ, Name: name}, nil
}

// KubernetesTarget is the addressing shared by every kubernetes check: which
// object, in which namespace, in which cluster.
//
// Cluster is optional. When empty the runbook's environment names the cluster,
// which is the common case: a runbook belongs to one environment and its checks
// inherit it.
type KubernetesTarget struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
}

func (t KubernetesTarget) validate() error {
	if t.Resource == "" {
		return fmt.Errorf("resource is required")
	}
	_, err := ParseResourceRef(t.Resource)
	return err
}

// ResourceExistsSpec asks whether a named object is still there. The plainest
// question in the catalogue and the one that catches the most rot.
type ResourceExistsSpec struct {
	KubernetesTarget `json:",inline"`
}

// Validate implements Spec.
func (s *ResourceExistsSpec) Validate() error { return s.validate() }

// FieldEqualsSpec asks whether a field still holds the value the runbook claims
// it holds: an image tag, a replica count, a storage class.
type FieldEqualsSpec struct {
	KubernetesTarget `json:",inline"`
	// Path is a dotted field path into the object, with numeric indices for
	// list elements: spec.template.spec.containers[0].image
	Path string `json:"path"`
	// Value is compared against the field. Its JSON type is significant: the
	// string "3" does not equal the number 3.
	Value json.RawMessage `json:"value"`
}

// Validate implements Spec.
func (s *FieldEqualsSpec) Validate() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Path == "" {
		return fmt.Errorf("path is required")
	}
	if len(s.Value) == 0 {
		return fmt.Errorf("value is required")
	}
	return nil
}

// FieldGTESpec asks whether a numeric field is at least the documented value:
// "we run at least three replicas" stays true as the deployment grows.
type FieldGTESpec struct {
	KubernetesTarget `json:",inline"`
	Path             string  `json:"path"`
	Value            float64 `json:"value"`
}

// Validate implements Spec.
func (s *FieldGTESpec) Validate() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Path == "" {
		return fmt.Errorf("path is required")
	}
	return nil
}

// CanISpec asks whether an identity may perform a verb: whether the person
// following this runbook still has the permission the runbook assumes.
//
// Unlike the other kubernetes checks, Resource here names a resource *type*
// ("deployments"), not a type/name pair: authorization.k8s.io reasons about
// types, and about one optional named object.
type CanISpec struct {
	Verb      string `json:"verb"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Cluster   string `json:"cluster,omitempty"`
	// Subresource asks about a subresource of the type: exec, log, status,
	// scale. "Can on-call exec into the database pod?" is one of the questions
	// an incident runbook most often assumes an answer to.
	//
	// It is a field of its own rather than kubectl's pods/exec spelling
	// because resource already carries type/name everywhere else in the
	// catalogue. A runbook writing deployment/payments-api here — copying the
	// pattern from resource_exists — would otherwise ask about a subresource
	// named payments-api, and the API's flat "no" would render as "your
	// on-call group cannot do this" when in fact it can.
	Subresource string `json:"subresource,omitempty"`
	// Name optionally narrows the review to a single named object.
	Name string `json:"name,omitempty"`
	// AsUser and AsGroup ask about another identity. Leaving both empty asks
	// about the probe's own identity via SelfSubjectAccessReview, which needs
	// no special grant; setting either requires create on subjectaccessreviews.
	// See docs/security.md.
	AsUser  string `json:"as_user,omitempty"`
	AsGroup string `json:"as_group,omitempty"`
}

// Validate implements Spec.
func (s *CanISpec) Validate() error {
	if s.Verb == "" {
		return fmt.Errorf("verb is required")
	}
	if s.Resource == "" {
		return fmt.Errorf("resource is required")
	}
	if strings.Contains(s.Resource, "/") {
		return fmt.Errorf("resource %q must be a resource type such as deployments, not type/name: name the object with the name field, and a subresource such as exec with the subresource field", s.Resource)
	}
	if strings.Contains(s.Subresource, "/") {
		return fmt.Errorf("subresource %q must be a bare name such as exec or status", s.Subresource)
	}
	if s.AsUser != "" && s.AsGroup != "" {
		return fmt.Errorf("set at most one of as_user and as_group")
	}
	return nil
}

// SubjectReview reports whether this check asks about an identity other than
// the probe's own, and therefore needs the privileged SubjectAccessReview
// rather than SelfSubjectAccessReview.
func (s *CanISpec) SubjectReview() bool {
	return s.AsUser != "" || s.AsGroup != ""
}

// SecretKeyPresentSpec asks whether a Secret still carries a named key.
//
// Existence only. The runner reads the key set and discards the values; the
// value is never read into a result, logged, hashed or transmitted.
type SecretKeyPresentSpec struct {
	KubernetesTarget `json:",inline"`
	Key              string `json:"key"`
}

// Validate implements Spec.
func (s *SecretKeyPresentSpec) Validate() error {
	if err := s.validate(); err != nil {
		return err
	}
	if s.Key == "" {
		return fmt.Errorf("key is required")
	}
	return nil
}
