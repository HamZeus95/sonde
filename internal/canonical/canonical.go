// Package canonical turns the resource a check references into a canonical URI.
//
// The reverse index joins on these strings: a PR that deletes
// deployment/payments-api canonicalises to the same URI the runbook's assertion
// did, or the PR bot stays silent. Both the CLI and the control plane's PR bot
// must therefore produce byte-identical output for the same input, which is why
// this logic lives in one package and why every function here is pure: same
// input, same URI, no network, no clock, no map iteration order.
//
// Changing a rule here invalidates every stored resource_refs row. Treat the
// forms below as a wire format, not an implementation detail.
package canonical

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
)

// ClusterScopedNamespace is the literal namespace segment used for resources
// that have no namespace, so that a URI always has the same number of segments.
const ClusterScopedNamespace = "_"

var (
	// ErrNotIndexable means the check names no concrete resource, so there is
	// nothing to put in the reverse index. can_i asks about a resource type and
	// parses asks about a command line; neither addresses one object. This is
	// an expected outcome, not a problem with the runbook.
	ErrNotIndexable = errors.New("check references no single resource")

	// ErrNoCluster means a kubernetes check could not be attributed to a
	// cluster: the check sets no cluster and the runbook declares no
	// environment. The check still executes; it just cannot be indexed.
	ErrNoCluster = errors.New("no cluster: set cluster on the check, or environment in the runbook frontmatter")

	// ErrNoRealm is ErrNoCluster's equivalent for identity checks.
	ErrNoRealm = errors.New("no realm: set realm on the check, or environment in the runbook frontmatter")

	// ErrNoRule means this package has no rule for a spec type: a check kind
	// was added without step 7 of docs/design.md §6. It is a bug in Sonde, not in
	// the runbook, and a test in this package fails on it before release.
	ErrNoRule = errors.New("no canonicalisation rule")
)

// ForCheck returns the canonical URI for the resource c references.
//
// It returns ErrNotIndexable when the check addresses no single resource, and
// ErrNoCluster/ErrNoRealm when the referenced resource cannot be placed. Callers
// surface those two as warnings, never as parse failures: an unindexable check
// is still a check worth running.
func ForCheck(meta model.Meta, c model.Check) (string, error) {
	switch spec := c.Spec.(type) {
	case *model.ResourceExistsSpec:
		return kubernetesURI(meta, spec.KubernetesTarget)
	case *model.FieldEqualsSpec:
		return kubernetesURI(meta, spec.KubernetesTarget)
	case *model.FieldGTESpec:
		return kubernetesURI(meta, spec.KubernetesTarget)
	case *model.SecretKeyPresentSpec:
		return kubernetesURI(meta, spec.KubernetesTarget)
	case *model.StatusIsSpec:
		return HTTP(spec.URL)
	case *model.RecordExistsSpec:
		return DNS(spec.Name, spec.Type), nil
	case *model.GroupExistsSpec:
		realm := firstNonEmpty(spec.Realm, meta.Environment)
		if realm == "" {
			return "", ErrNoRealm
		}
		return IdentityGroup(spec.Provider, realm, spec.Group), nil
	case *model.CanISpec:
		// can_i names a resource type, not an object. Indexing it under a
		// type-level URI would need a URI form that does not exist in the
		// spec yet; see the open question in docs/design.md §16.
		return "", ErrNotIndexable
	case *model.ParsesSpec:
		// Which objects a command names is decided by parsing the command at
		// execution time, not here.
		return "", ErrNotIndexable
	default:
		return "", fmt.Errorf("%w for %s/%s: add one in internal/canonical", ErrNoRule, c.Kind, c.Check)
	}
}

func kubernetesURI(meta model.Meta, t model.KubernetesTarget) (string, error) {
	cluster := firstNonEmpty(t.Cluster, meta.Environment)
	if cluster == "" {
		return "", ErrNoCluster
	}
	ref, err := model.ParseResourceRef(t.Resource)
	if err != nil {
		return "", err
	}
	return Kubernetes(cluster, t.Namespace, ref.Type, ref.Name), nil
}

// Kubernetes builds k8s://<cluster>/<namespace>/<kind>/<name>. An empty
// namespace becomes the cluster-scoped marker.
func Kubernetes(cluster, namespace, kind, name string) string {
	if namespace == "" {
		namespace = ClusterScopedNamespace
	}
	return fmt.Sprintf("k8s://%s/%s/%s/%s",
		strings.ToLower(strings.TrimSpace(cluster)),
		strings.ToLower(strings.TrimSpace(namespace)),
		NormaliseKind(kind),
		strings.TrimSpace(name))
}

// HTTP builds http://<host><path>.
//
// The scheme is always http because in a canonical URI the scheme names the
// provider, the way k8s:// and dns:// do, not the wire protocol. A dashboard
// linked as https:// in one runbook and http:// in another is one resource, and
// resource_refs.provider says "http" for both.
//
// Query strings and fragments are stripped: they address a view of a resource,
// not the resource. Default ports are dropped so that :443 and the absent port
// agree.
func HTTP(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url %q has no host", raw)
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !defaultPort(u.Scheme, port) {
		host = host + ":" + port
	}
	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	path = strings.TrimSuffix(path, "/")
	return "http://" + host + path, nil
}

func defaultPort(scheme, port string) bool {
	return (scheme == "http" && port == "80") || (scheme == "https" && port == "443")
}

// DNS builds dns://<name>/<TYPE>. The name is lowercased and its root dot
// dropped so that db-primary.internal and db-primary.internal. are one resource.
func DNS(name, recordType string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	return fmt.Sprintf("dns://%s/%s", name, strings.ToUpper(strings.TrimSpace(recordType)))
}

// IdentityGroup builds idp://<provider>/<realm>/group/<name>.
func IdentityGroup(provider, realm, group string) string {
	return fmt.Sprintf("idp://%s/%s/group/%s",
		strings.ToLower(strings.TrimSpace(provider)),
		strings.ToLower(strings.TrimSpace(realm)),
		strings.TrimSpace(group))
}

// AWS builds aws://<account>/<region>/<service>/<type>/<id>.
//
// No v1 check emits an AWS URI. It is here because the PR bot canonicalises
// resources out of an OpenTofu plan before any AWS check exists, and both sides
// must use the same builder.
func AWS(account, region, service, resourceType, id string) string {
	return fmt.Sprintf("aws://%s/%s/%s/%s/%s",
		strings.ToLower(strings.TrimSpace(account)),
		strings.ToLower(strings.TrimSpace(region)),
		strings.ToLower(strings.TrimSpace(service)),
		strings.ToLower(strings.TrimSpace(resourceType)),
		strings.TrimSpace(id))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
