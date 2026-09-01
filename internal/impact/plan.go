package impact

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/HamZeus95/sonde/internal/canonical"
)

// planDocument is the part of `tofu show -json` this package reads.
type planDocument struct {
	FormatVersion   string           `json:"format_version"`
	ResourceChanges []resourceChange `json:"resource_changes"`
}

type resourceChange struct {
	Address      string `json:"address"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	ProviderName string `json:"provider_name"`
	Change       struct {
		Actions []string       `json:"actions"`
		Before  map[string]any `json:"before"`
	} `json:"change"`
}

// deletes reports whether this change removes the resource for good.
//
// A replacement — delete then create — is not a removal: the resource is still
// there when the plan finishes, and warning that a runbook depends on it would
// be noise of exactly the kind that gets a bot muted.
func (c resourceChange) deletes() bool {
	var hasDelete, hasCreate bool
	for _, action := range c.Change.Actions {
		switch action {
		case "delete":
			hasDelete = true
		case "create":
			hasCreate = true
		}
	}
	return hasDelete && !hasCreate
}

// Plan reads an OpenTofu or Terraform plan in JSON form and reports what it
// removes.
//
// Produce the input with `tofu show -json tfplan > plan.json`.
func Plan(path, cluster string) (Report, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the caller named
	if err != nil {
		return Report{}, fmt.Errorf("read %s: %w", path, err)
	}
	var document planDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return Report{}, fmt.Errorf("parse %s as a plan: %w", path, err)
	}

	var (
		removed    []Removal
		unmappable []Unmappable
	)
	for _, change := range document.ResourceChanges {
		if !change.deletes() {
			continue
		}
		removal, reason := canonicaliseChange(change, cluster)
		if reason != "" {
			unmappable = append(unmappable, Unmappable{
				Source: change.Address,
				Kind:   change.Type,
				Reason: reason,
			})
			continue
		}
		removal.Source = change.Address
		removed = append(removed, removal)
	}
	return newReport(removed, unmappable), nil
}

// canonicaliseChange maps one deleted resource to a canonical URI, or explains
// why it cannot.
//
// The three families here are the answer to the open question in docs/design.md §16
// about which resource types to map first: the Kubernetes provider, because
// that is what runbooks name most; Route 53 and Cloudflare records, because a
// failover runbook is mostly DNS; and anything with an ARN, because an ARN
// already carries every segment an aws:// URI needs.
func canonicaliseChange(change resourceChange, cluster string) (Removal, string) {
	switch {
	case change.Type == "kubernetes_manifest":
		return kubernetesManifest(change, cluster)
	case strings.HasPrefix(change.Type, "kubernetes_"):
		return kubernetesResource(change, cluster)
	case change.Type == "aws_route53_record" || change.Type == "cloudflare_record":
		return dnsRecord(change)
	case strings.HasPrefix(change.Type, "aws_"):
		return awsResource(change)
	default:
		return Removal{}, fmt.Sprintf("no canonical URI form for %s", change.Type)
	}
}

// kubernetesResource maps the typed resources — kubernetes_deployment and its
// kin. Terraform renders a block as a single-element list, so metadata is
// metadata[0].
func kubernetesResource(change resourceChange, cluster string) (Removal, string) {
	kind := strings.TrimPrefix(change.Type, "kubernetes_")
	kind = stripVersionSuffix(kind)
	kind = strings.ReplaceAll(kind, "_", "")

	name, namespace := metadataOf(change.Change.Before)
	if name == "" {
		return Removal{}, "the plan carries no name for this resource"
	}
	return kubernetesRemoval(cluster, namespace, kind, name), ""
}

// kubernetesManifest maps the generic resource, which carries the object itself.
func kubernetesManifest(change resourceChange, cluster string) (Removal, string) {
	manifest, ok := change.Change.Before["manifest"].(map[string]any)
	if !ok {
		return Removal{}, "the plan carries no manifest for this resource"
	}
	kind, _ := manifest["kind"].(string)
	metadata, _ := manifest["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	namespace, _ := metadata["namespace"].(string)
	if kind == "" || name == "" {
		return Removal{}, "the manifest has no kind or name"
	}
	return kubernetesRemoval(cluster, namespace, kind, name), ""
}

func kubernetesRemoval(cluster, namespace, kind, name string) Removal {
	if namespace == "" {
		namespace = canonical.ClusterScopedNamespace
	}
	removal := Removal{
		Provider:  "k8s",
		Cluster:   cluster,
		Namespace: namespace,
		Type:      canonical.NormaliseKind(kind),
		Name:      name,
	}
	if cluster != "" {
		removal.Canonical = canonical.Kubernetes(cluster, namespace, kind, name)
	}
	return removal
}

// metadataOf reads name and namespace out of a Terraform block, which the plan
// JSON renders as a one-element list.
func metadataOf(before map[string]any) (name, namespace string) {
	list, ok := before["metadata"].([]any)
	if !ok || len(list) == 0 {
		return "", ""
	}
	metadata, ok := list[0].(map[string]any)
	if !ok {
		return "", ""
	}
	name, _ = metadata["name"].(string)
	namespace, _ = metadata["namespace"].(string)
	return name, namespace
}

// stripVersionSuffix drops the _v1 and _v2 the Kubernetes provider appends to
// its newer resource names, so kubernetes_deployment_v1 is a deployment.
func stripVersionSuffix(kind string) string {
	parts := strings.Split(kind, "_")
	last := parts[len(parts)-1]
	if len(last) >= 2 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		return strings.Join(parts[:len(parts)-1], "_")
	}
	return kind
}

func dnsRecord(change resourceChange) (Removal, string) {
	name, _ := change.Change.Before["name"].(string)
	recordType, _ := change.Change.Before["type"].(string)
	if name == "" || recordType == "" {
		return Removal{}, "the plan carries no name or type for this record"
	}
	return Removal{
		Provider:  "dns",
		Type:      strings.ToUpper(recordType),
		Name:      strings.TrimSuffix(strings.ToLower(name), "."),
		Canonical: canonical.DNS(name, recordType),
	}, ""
}

// awsResource maps anything whose plan carries an ARN, because an ARN already
// names the account, region, service, type and id an aws:// URI is made of.
// A resource without one cannot be placed, and says so rather than being
// dropped.
func awsResource(change resourceChange) (Removal, string) {
	arn, _ := change.Change.Before["arn"].(string)
	if arn == "" {
		return Removal{}, "the plan carries no ARN, so the account and region are unknown"
	}
	parsed, err := parseARN(arn, change.Type)
	if err != nil {
		return Removal{}, err.Error()
	}
	return parsed, ""
}

// parseARN splits arn:partition:service:region:account:resource.
func parseARN(arn, terraformType string) (Removal, error) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return Removal{}, fmt.Errorf("%q is not an ARN this build understands", arn)
	}
	service, region, account, resource := parts[2], parts[3], parts[4], parts[5]
	if region == "" || account == "" {
		return Removal{}, fmt.Errorf("the ARN names no region or account")
	}

	resourceType, id := splitARNResource(resource)
	if resourceType == "" {
		// A bare id: fall back to the Terraform type with its provider and
		// service prefixes removed, so aws_s3_bucket becomes bucket.
		resourceType = strings.TrimPrefix(strings.TrimPrefix(terraformType, "aws_"), service+"_")
	}
	return Removal{
		Provider:  "aws",
		Cluster:   account,
		Namespace: region,
		Type:      resourceType,
		Name:      id,
		Canonical: canonical.AWS(account, region, service, resourceType, id),
	}, nil
}

// splitARNResource handles both "type/id" and "type:id", and a bare id.
func splitARNResource(resource string) (resourceType, id string) {
	if typ, name, found := strings.Cut(resource, "/"); found {
		return typ, name
	}
	if typ, name, found := strings.Cut(resource, ":"); found {
		return typ, name
	}
	return "", resource
}
