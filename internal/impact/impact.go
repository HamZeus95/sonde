// Package impact works out which infrastructure resources a change removes.
//
// It is the other half of the reverse index. The parser turns runbooks into
// canonical resource URIs; this turns a pull request into the same URIs, and
// the control plane joins the two to answer the question no other tool asks:
// which documented procedures does this change break?
//
// It lives in Go, beside internal/canonical, for the reason §7 gives — the CLI
// and the control plane's PR bot must canonicalise identically. The bot
// consumes this package's JSON output the same way the control plane consumes
// the parser's.
package impact

import (
	"fmt"
	"sort"

	"github.com/HamZeus95/sonde/internal/model"
)

// SchemaVersion is the version of the JSON document this package emits.
const SchemaVersion = model.SchemaVersion

// Report is what `sonde impact` prints.
type Report struct {
	SchemaVersion int `json:"schema_version"`
	// Removed lists the resources the change deletes.
	Removed []Removal `json:"removed"`
	// Unmappable lists resources the change deletes that cannot be expressed
	// as a canonical URI. They are reported rather than dropped: a resource
	// nobody can look up is a gap in the index, and the bot should be able to
	// say so instead of quietly finding nothing.
	Unmappable []Unmappable `json:"unmappable,omitempty"`
}

// Removal is one resource a change deletes.
type Removal struct {
	// Canonical is the full URI, empty when the cluster is not known — a
	// manifest names no cluster, and a repository that has not said which
	// environment it deploys to leaves that blank. The components below are
	// then what the index is queried on.
	Canonical string `json:"canonical,omitempty"`
	Provider  string `json:"provider"`
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Type      string `json:"type,omitempty"`
	Name      string `json:"name,omitempty"`
	// Source is the file or plan address the removal was found in, so a
	// comment can point at the line that caused it.
	Source string `json:"source"`
}

// Unmappable is a deletion this build cannot express as a canonical URI.
type Unmappable struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// key identifies a removal for de-duplication: the same object deleted from two
// files, or named by both a manifest and a plan, is one removal.
func (r Removal) key() string {
	return fmt.Sprintf("%s|%s|%s|%s|%s", r.Provider, r.Cluster, r.Namespace, r.Type, r.Name)
}

// newReport de-duplicates and orders a report so that the same change always
// produces the same bytes — a PR comment that reorders itself on every run is a
// PR comment people mute.
func newReport(removed []Removal, unmappable []Unmappable) Report {
	seen := map[string]bool{}
	unique := make([]Removal, 0, len(removed))
	for _, r := range removed {
		if seen[r.key()] {
			continue
		}
		seen[r.key()] = true
		unique = append(unique, r)
	}
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].key() != unique[j].key() {
			return unique[i].key() < unique[j].key()
		}
		return unique[i].Source < unique[j].Source
	})
	sort.Slice(unmappable, func(i, j int) bool {
		if unmappable[i].Source != unmappable[j].Source {
			return unmappable[i].Source < unmappable[j].Source
		}
		return unmappable[i].Kind < unmappable[j].Kind
	})
	return Report{SchemaVersion: SchemaVersion, Removed: unique, Unmappable: unmappable}
}
