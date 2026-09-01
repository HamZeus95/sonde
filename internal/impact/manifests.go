package impact

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/HamZeus95/sonde/internal/canonical"
)

// object is the part of a Kubernetes manifest needed to identify it. Nothing
// else is read: this package decides what a change removes, not what it does.
type object struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
}

// identity is what makes two manifests the same object across a diff.
func (o object) identity() string {
	return strings.Join([]string{
		canonical.NormaliseKind(o.Kind),
		o.Metadata.Namespace,
		o.Metadata.Name,
	}, "|")
}

// valid reports whether a YAML document is a Kubernetes object at all. A
// repository is full of YAML that is not — CI workflows, Helm values, this
// project's own golden files — and none of it should look like a deletion.
func (o object) valid() bool {
	return o.Kind != "" && o.Metadata.Name != "" && o.APIVersion != ""
}

// Manifests reports the Kubernetes objects present in before and absent from
// after.
//
// Both paths may be a file or a directory. The bot writes the base and head
// versions of a pull request's changed files into two trees and points this at
// them; the diff is by object identity, not by file, so moving an object
// between files is correctly not a deletion.
func Manifests(beforePath, afterPath, cluster string) (Report, error) {
	before, err := collect(beforePath)
	if err != nil {
		return Report{}, err
	}
	after, err := collect(afterPath)
	if err != nil {
		return Report{}, err
	}

	var removed []Removal
	for id, found := range before {
		if _, kept := after[id]; kept {
			continue
		}
		removed = append(removed, Removal{
			Canonical: canonicalOrEmpty(cluster, found.object),
			Provider:  "k8s",
			Cluster:   cluster,
			Namespace: namespaceOf(found.object),
			Type:      canonical.NormaliseKind(found.object.Kind),
			Name:      found.object.Metadata.Name,
			Source:    found.source,
		})
	}
	return newReport(removed, nil), nil
}

// canonicalOrEmpty builds the URI when the cluster is known. When it is not —
// a manifest names no cluster — the components in the Removal are what the
// index is queried on instead, which is exactly what resource_refs stores them
// for.
func canonicalOrEmpty(cluster string, o object) string {
	if cluster == "" {
		return ""
	}
	return canonical.Kubernetes(cluster, o.Metadata.Namespace, o.Kind, o.Metadata.Name)
}

// namespaceOf reports the namespace a manifest declares, using the
// cluster-scoped marker when it declares none, so that the value matches what
// the parser wrote into the index for the same object.
func namespaceOf(o object) string {
	if o.Metadata.Namespace == "" {
		return canonical.ClusterScopedNamespace
	}
	return o.Metadata.Namespace
}

type found struct {
	object object
	source string
}

// collect reads every Kubernetes object under a path. A path that does not
// exist is an empty set rather than an error: a pull request that adds a file
// has no "before" for it.
func collect(root string) (map[string]found, error) {
	objects := map[string]found{}
	info, err := os.Stat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return objects, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return objects, readFile(root, filepath.Base(root), objects)
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) && path != root {
				return fs.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		return readFile(path, filepath.ToSlash(relative), objects)
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	return objects, nil
}

// readFile adds every Kubernetes object in one file.
//
// A file that is not parseable YAML is skipped rather than failing the run: a
// pull request touching a templated Helm chart or a broken file should not stop
// the bot from reporting the deletions it can see.
func readFile(path, source string, objects map[string]found) error {
	raw, err := os.ReadFile(path) //nolint:gosec // a path the caller named
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	reader := k8syaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(raw)))
	for {
		document, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return nil //nolint:nilerr // an unreadable file is not a deletion
		}
		if len(bytes.TrimSpace(document)) == 0 {
			continue
		}
		var o object
		if err := yaml.Unmarshal(document, &o); err != nil {
			continue
		}
		if !o.valid() {
			continue
		}
		objects[o.identity()] = found{object: o, source: source}
	}
}

func isYAML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

// skipDir keeps the walker out of directories that hold YAML but never
// deployed manifests.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".terraform", "charts":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "."
}
