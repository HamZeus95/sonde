// Package kubernetes executes kubernetes checks against a cluster's API.
//
// Every request this package makes is a read — get, or a SelfSubjectAccessReview
// / SubjectAccessReview, which the Kubernetes API models as a create but which
// persists nothing and changes nothing (see accessReview below). There is no
// code path here that can write to a cluster.
package kubernetes

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/HamZeus95/sonde/internal/canonical"
)

// Target addresses one object.
type Target struct {
	Cluster   string
	Namespace string
	// Resource is the type as the runbook spells it: deployment, deployments,
	// deploy, deployment.apps.
	Resource string
	Name     string
}

// AccessRequest is one authorization question.
type AccessRequest struct {
	Cluster     string
	Verb        string
	Resource    string
	Subresource string
	Namespace   string
	Name        string
	// User and Groups ask about another identity. Both empty asks about the
	// caller's own identity, which needs no special grant.
	User   string
	Groups []string
}

// SubjectReview reports whether answering this question needs the privileged
// SubjectAccessReview rather than SelfSubjectAccessReview.
func (r AccessRequest) SubjectReview() bool { return r.User != "" || len(r.Groups) > 0 }

// AccessResult is the API's answer.
type AccessResult struct {
	Allowed bool
	Denied  bool
	Reason  string
}

// Client is the cluster access this package's runners need. It is declared here,
// by the consumer, so the runners can be tested against a fake without a
// cluster and without client-go in the test.
type Client interface {
	// Get returns one object. A missing object is reported through
	// apierrors.IsNotFound, not through a nil object.
	Get(ctx context.Context, target Target) (*unstructured.Unstructured, error)
	// Access answers one authorization question.
	Access(ctx context.Context, req AccessRequest) (AccessResult, error)
}

// ClientSet is the real Client: one lazily built connection per cluster.
type ClientSet struct {
	loadingRules *clientcmd.ClientConfigLoadingRules
	// contextOverride pins every check to one kubeconfig context regardless of
	// the cluster its runbook names. It is what makes the single-cluster case
	// a one-flag operation.
	contextOverride string

	mu       sync.Mutex
	raw      *rawConfig
	clusters map[string]*cluster
}

type cluster struct {
	dynamic dynamic.Interface
	typed   clientset.Interface
	mapper  meta.RESTMapper
}

type rawConfig struct {
	contexts       []string
	currentContext string
}

// NewClientSet builds a client set from a kubeconfig. An empty kubeconfigPath
// means the usual discovery: $KUBECONFIG, then ~/.kube/config.
func NewClientSet(kubeconfigPath, contextOverride string) *ClientSet {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	return &ClientSet{
		loadingRules:    rules,
		contextOverride: contextOverride,
		clusters:        map[string]*cluster{},
	}
}

// Contexts lists the kubeconfig contexts available, for error messages that
// tell the user what they could have meant.
func (c *ClientSet) Contexts() ([]string, error) {
	raw, err := c.rawConfig()
	if err != nil {
		return nil, err
	}
	return raw.contexts, nil
}

func (c *ClientSet) rawConfig() (*rawConfig, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.raw != nil {
		return c.raw, nil
	}
	loaded, err := c.loadingRules.Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	contexts := make([]string, 0, len(loaded.Contexts))
	for name := range loaded.Contexts {
		contexts = append(contexts, name)
	}
	sort.Strings(contexts)
	c.raw = &rawConfig{contexts: contexts, currentContext: loaded.CurrentContext}
	return c.raw, nil
}

// contextFor maps the cluster a runbook names to a kubeconfig context.
//
// It refuses to guess. Running a runbook's prod-eu-1 checks against whichever
// cluster happens to be current would produce results that look authoritative
// and describe the wrong cluster, which is worse than no results.
func (c *ClientSet) contextFor(clusterName string) (string, error) {
	if c.contextOverride != "" {
		return c.contextOverride, nil
	}
	raw, err := c.rawConfig()
	if err != nil {
		return "", err
	}
	if clusterName == "" {
		if raw.currentContext == "" {
			return "", fmt.Errorf("kubeconfig has no current context")
		}
		return raw.currentContext, nil
	}
	for _, name := range raw.contexts {
		if name == clusterName {
			return name, nil
		}
	}
	return "", fmt.Errorf("no kubeconfig context named %q (available: %s); pass --context to run against one cluster regardless of the name the runbook uses",
		clusterName, strings.Join(raw.contexts, ", "))
}

func (c *ClientSet) clusterFor(clusterName string) (*cluster, error) {
	contextName, err := c.contextFor(clusterName)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.clusters[contextName]; ok {
		return existing, nil
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		c.loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: contextName},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig context %s: %w", contextName, err)
	}
	config.UserAgent = "sonde/1"

	built, err := newCluster(config)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", contextName, err)
	}
	c.clusters[contextName] = built
	return built, nil
}

// NewClusterFromConfig builds a single-cluster Client from a rest.Config, which
// is what the envtest-based tests use.
func NewClusterFromConfig(config *rest.Config) (Client, error) {
	built, err := newCluster(config)
	if err != nil {
		return nil, err
	}
	return &fixedCluster{cluster: built}, nil
}

type fixedCluster struct{ cluster *cluster }

func (f *fixedCluster) Get(ctx context.Context, target Target) (*unstructured.Unstructured, error) {
	return f.cluster.get(ctx, target)
}

func (f *fixedCluster) Access(ctx context.Context, req AccessRequest) (AccessResult, error) {
	return f.cluster.access(ctx, req)
}

func newCluster(config *rest.Config) (*cluster, error) {
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	typed, err := clientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	return &cluster{
		dynamic: dyn,
		typed:   typed,
		mapper:  restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient)),
	}, nil
}

// Get implements Client.
func (c *ClientSet) Get(ctx context.Context, target Target) (*unstructured.Unstructured, error) {
	cl, err := c.clusterFor(target.Cluster)
	if err != nil {
		return nil, err
	}
	return cl.get(ctx, target)
}

// Access implements Client.
func (c *ClientSet) Access(ctx context.Context, req AccessRequest) (AccessResult, error) {
	cl, err := c.clusterFor(req.Cluster)
	if err != nil {
		return AccessResult{}, err
	}
	return cl.access(ctx, req)
}

func (c *cluster) get(ctx context.Context, target Target) (*unstructured.Unstructured, error) {
	mapping, err := c.mappingFor(target.Resource)
	if err != nil {
		return nil, err
	}
	client := c.dynamic.Resource(mapping.Resource)
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		namespace := target.Namespace
		if namespace == "" {
			namespace = metav1.NamespaceDefault
		}
		return client.Namespace(namespace).Get(ctx, target.Name, metav1.GetOptions{})
	}
	return client.Get(ctx, target.Name, metav1.GetOptions{})
}

// mappingFor resolves a runbook's spelling of a resource type to a REST
// mapping. The alias table in internal/canonical does the kubectl short names,
// so that a runbook's "deploy" and the reverse index's "deployment" resolve
// through the same rules.
func (c *cluster) mappingFor(resource string) (*meta.RESTMapping, error) {
	normalised := canonical.NormaliseKind(resource)
	group := ""
	if _, groupPart, found := strings.Cut(strings.ToLower(resource), "."); found {
		group = groupPart
	}
	gvr, err := c.mapper.ResourceFor(schema.GroupVersionResource{Group: group, Resource: normalised})
	if err != nil {
		return nil, fmt.Errorf("resolve resource type %q: %w", resource, err)
	}
	gvk, err := c.mapper.KindFor(gvr)
	if err != nil {
		return nil, fmt.Errorf("resolve kind for %q: %w", resource, err)
	}
	mapping, err := c.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, fmt.Errorf("rest mapping for %q: %w", resource, err)
	}
	return mapping, nil
}

// access answers an authorization question.
//
// Both review kinds are submitted with create. This is not a mutation: the
// Kubernetes API models a question as a POST whose request body is the question
// and whose response body is the answer. Nothing is persisted and nothing in
// the cluster changes. It is the one create verb Sonde is permitted to use, and
// docs/security.md explains it to reviewers who will, correctly, ask.
func (c *cluster) access(ctx context.Context, req AccessRequest) (AccessResult, error) {
	// The API group has to be resolved, not assumed. A review for "deployments"
	// with an empty group asks about a core-group resource that does not exist,
	// and the answer to that is a flat "no" — which would render as "your
	// on-call group cannot do this" when in fact it can. Discovery decides.
	mapping, err := c.mappingFor(req.Resource)
	if err != nil {
		return AccessResult{}, err
	}
	attributes := &authorizationv1.ResourceAttributes{
		Namespace:   req.Namespace,
		Verb:        req.Verb,
		Group:       mapping.Resource.Group,
		Resource:    mapping.Resource.Resource,
		Subresource: req.Subresource,
		Name:        req.Name,
	}

	if !req.SubjectReview() {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: attributes},
		}
		answer, err := c.typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			return AccessResult{}, fmt.Errorf("selfsubjectaccessreview: %w", err)
		}
		return AccessResult{Allowed: answer.Status.Allowed, Denied: answer.Status.Denied, Reason: answer.Status.Reason}, nil
	}

	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			ResourceAttributes: attributes,
			User:               req.User,
			Groups:             req.Groups,
		},
	}
	answer, err := c.typed.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsForbidden(err) {
			return AccessResult{}, fmt.Errorf("subjectaccessreview requires create on subjectaccessreviews, which this identity does not have (see docs/security.md): %w", err)
		}
		return AccessResult{}, fmt.Errorf("subjectaccessreview: %w", err)
	}
	return AccessResult{Allowed: answer.Status.Allowed, Denied: answer.Status.Denied, Reason: answer.Status.Reason}, nil
}
