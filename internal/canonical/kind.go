package canonical

import "strings"

// kindAliases maps the spellings that appear in real runbooks — kubectl short
// names and plurals — to the lowercase singular form a canonical URI uses.
//
// It is a fixed table rather than a discovery call because canonicalisation is
// pure: the PR bot canonicalises a deleted resource out of a Terraform plan with
// no cluster to ask. A type that is not in the table is lowercased and left
// alone; guessing a singular by stripping an s turns ingress into ingres and
// puts a runbook in the index under a name nothing else will ever produce.
var kindAliases = map[string]string{
	"po": "pod", "pod": "pod", "pods": "pod",
	"svc": "service", "service": "service", "services": "service",
	"deploy": "deployment", "deployment": "deployment", "deployments": "deployment",
	"sts": "statefulset", "statefulset": "statefulset", "statefulsets": "statefulset",
	"ds": "daemonset", "daemonset": "daemonset", "daemonsets": "daemonset",
	"rs": "replicaset", "replicaset": "replicaset", "replicasets": "replicaset",
	"job": "job", "jobs": "job",
	"cj": "cronjob", "cronjob": "cronjob", "cronjobs": "cronjob",
	"cm": "configmap", "configmap": "configmap", "configmaps": "configmap",
	"secret": "secret", "secrets": "secret",
	"ing": "ingress", "ingress": "ingress", "ingresses": "ingress",
	"netpol": "networkpolicy", "networkpolicy": "networkpolicy", "networkpolicies": "networkpolicy",
	"ns": "namespace", "namespace": "namespace", "namespaces": "namespace",
	"no": "node", "node": "node", "nodes": "node",
	"pvc": "persistentvolumeclaim", "persistentvolumeclaim": "persistentvolumeclaim", "persistentvolumeclaims": "persistentvolumeclaim",
	"pv": "persistentvolume", "persistentvolume": "persistentvolume", "persistentvolumes": "persistentvolume",
	"sa": "serviceaccount", "serviceaccount": "serviceaccount", "serviceaccounts": "serviceaccount",
	"ep": "endpoints", "endpoint": "endpoints", "endpoints": "endpoints",
	"hpa": "horizontalpodautoscaler", "horizontalpodautoscaler": "horizontalpodautoscaler", "horizontalpodautoscalers": "horizontalpodautoscaler",
	"pdb": "poddisruptionbudget", "poddisruptionbudget": "poddisruptionbudget", "poddisruptionbudgets": "poddisruptionbudget",
	"sc": "storageclass", "storageclass": "storageclass", "storageclasses": "storageclass",
	"crd": "customresourcedefinition", "customresourcedefinition": "customresourcedefinition", "customresourcedefinitions": "customresourcedefinition",
	"role": "role", "roles": "role",
	"rolebinding": "rolebinding", "rolebindings": "rolebinding",
	"clusterrole": "clusterrole", "clusterroles": "clusterrole",
	"clusterrolebinding": "clusterrolebinding", "clusterrolebindings": "clusterrolebinding",
}

// NormaliseKind lowercases a resource type, drops any API group suffix
// (deployment.apps becomes deployment) and resolves known aliases to the
// singular form.
func NormaliseKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if group := strings.Index(k, "."); group > 0 {
		k = k[:group]
	}
	if singular, ok := kindAliases[k]; ok {
		return singular
	}
	return k
}
