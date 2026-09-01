// Package suggest drafts assertion blocks for a runbook that has none.
//
// This is the one place in Sonde where a language model is used, and it is
// deliberately fenced off from everything else (docs/design.md §3.5). A model that
// *believes* a deployment exists is worse than useless in a product whose
// output is compliance evidence, so nothing here executes, verifies, or
// decides anything: it drafts blocks, this package validates them against the
// real parser, and a human reads the diff and commits it.
//
// The model never sees a cluster, a credential or a result. It sees prose.
package suggest

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/HamZeus95/sonde/internal/model"
	"github.com/HamZeus95/sonde/internal/parse"
)

// Request is what a drafter is asked to work from.
type Request struct {
	// Path is the runbook's path, for the model's orientation only.
	Path string
	// Meta is the runbook's frontmatter, when it has any.
	Meta model.Meta
	// Prose is the document with its existing sonde blocks removed, so the
	// model drafts from the steps rather than from the answers.
	Prose string
	// Headings are the document's headings, in order. A draft anchors itself
	// to one by index, which is the only placement instruction accepted:
	// an index either exists or it does not, and there is nothing to
	// misinterpret.
	Headings []Heading
	// Existing lists the checks already in the document, so the model is not
	// asked to invent what is already there.
	Existing []ExistingCheck
	// Catalogue is the set of kind+check pairs and the fields each accepts.
	Catalogue []CatalogueEntry
}

// Heading is one Markdown heading.
type Heading struct {
	Index int    `json:"index"`
	Level int    `json:"level"`
	Text  string `json:"text"`
	// line is the 1-indexed line the heading is on, and endLine the last line
	// of its section. Unexported: placement is this package's business.
	line    int
	endLine int
}

// ExistingCheck is an assertion the runbook already makes.
type ExistingCheck struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Check string `json:"check"`
	// Canonical is the resource it references, when it names one.
	Canonical string `json:"canonical,omitempty"`
}

// CatalogueEntry describes one kind+check pair to the model.
type CatalogueEntry struct {
	Kind    string   `json:"kind"`
	Check   string   `json:"check"`
	Answers string   `json:"answers"`
	Fields  []string `json:"fields"`
}

// Draft is one proposed assertion, as a drafter returns it.
type Draft struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Check        string `json:"check"`
	HeadingIndex int    `json:"heading_index"`
	// Fields is the YAML body of the block, without id, kind or check: those
	// are assembled here so the block's shape is this package's, not the
	// model's.
	Fields string `json:"fields"`
	// Rationale is the sentence shown to the human reviewing the diff. A
	// suggestion nobody can evaluate is a suggestion nobody should accept.
	Rationale string `json:"rationale"`
}

// Drafter turns a request into drafts. The Anthropic client implements it;
// tests use a fake, so the test suite never calls an API and never needs a key.
type Drafter interface {
	Draft(ctx context.Context, req Request) ([]Draft, error)
}

// Suggestion is a draft that survived validation, with where it goes.
type Suggestion struct {
	Draft Draft `json:"draft"`
	// Block is the rendered fenced block, ready to insert.
	Block string `json:"block"`
	// AfterLine is the 1-indexed line it is inserted after.
	AfterLine int `json:"after_line"`
	// Heading is the section it was placed in.
	Heading string `json:"heading"`
}

// Rejection is a draft that did not survive, and why.
//
// Rejections are reported rather than dropped. A model that proposes something
// unusable is information about the model and about the runbook, and hiding it
// would make the command look more reliable than it is.
type Rejection struct {
	Draft  Draft  `json:"draft"`
	Reason string `json:"reason"`
}

// Result is what the command prints.
type Result struct {
	SchemaVersion int          `json:"schema_version"`
	Path          string       `json:"path"`
	Suggestions   []Suggestion `json:"suggestions"`
	Rejected      []Rejection  `json:"rejected,omitempty"`
}

// Analyse reads a runbook and builds the request a drafter works from.
func Analyse(path string, source []byte) (Request, error) {
	headings, err := headingsOf(source)
	if err != nil {
		return Request{}, err
	}

	request := Request{
		Path:      path,
		Prose:     string(stripBlocks(source)),
		Headings:  headings,
		Catalogue: catalogue(),
	}

	// A document that is already a runbook contributes its frontmatter and its
	// existing checks; one that is not yet a runbook contributes neither, and
	// the caller is told to add frontmatter first.
	runbook, err := parse.Source(path, source)
	if err != nil {
		return Request{}, err
	}
	if runbook != nil {
		request.Meta = runbook.Meta
		for _, check := range runbook.Checks {
			request.Existing = append(request.Existing, ExistingCheck{
				ID:        check.ID,
				Kind:      string(check.Kind),
				Check:     check.Check,
				Canonical: check.Canonical,
			})
		}
	}
	return request, nil
}

// catalogue describes every check to the model, including which fields each
// one accepts, so it drafts from the real contract rather than from memory.
func catalogue() []CatalogueEntry {
	var out []CatalogueEntry
	for _, def := range model.Catalogue() {
		fields := model.SpecFields(def.New())
		sort.Strings(fields)
		out = append(out, CatalogueEntry{
			Kind:    string(def.Kind),
			Check:   def.Check,
			Answers: def.Summary,
			Fields:  fields,
		})
	}
	return out
}

// Validate turns drafts into suggestions, rejecting anything that would not
// parse, would duplicate an existing check, or points nowhere.
//
// Everything the model produces goes through the real parser before a human
// sees it. A suggestion that does not parse is not a suggestion.
func Validate(request Request, source []byte, drafts []Draft) Result {
	result := Result{SchemaVersion: model.SchemaVersion, Path: request.Path}

	taken := map[string]bool{}
	for _, existing := range request.Existing {
		taken[existing.ID] = true
	}

	for _, draft := range drafts {
		block, afterLine, heading, reason := place(request, draft, taken)
		if reason != "" {
			result.Rejected = append(result.Rejected, Rejection{Draft: draft, Reason: reason})
			continue
		}
		if reason := parses(request, source, block); reason != "" {
			result.Rejected = append(result.Rejected, Rejection{Draft: draft, Reason: reason})
			continue
		}
		taken[draft.ID] = true
		result.Suggestions = append(result.Suggestions, Suggestion{
			Draft: draft, Block: block, AfterLine: afterLine, Heading: heading,
		})
	}

	// Applied bottom-up so that inserting one block does not move the line
	// another was anchored to.
	sort.SliceStable(result.Suggestions, func(i, j int) bool {
		return result.Suggestions[i].AfterLine < result.Suggestions[j].AfterLine
	})
	return result
}

func place(request Request, draft Draft, taken map[string]bool) (block string, afterLine int, heading, reason string) {
	if draft.ID == "" {
		return "", 0, "", "the draft has no id"
	}
	if taken[draft.ID] {
		return "", 0, "", fmt.Sprintf("the runbook already has a check called %q", draft.ID)
	}
	if _, ok := model.Lookup(model.Kind(draft.Kind), draft.Check); !ok {
		return "", 0, "", (&model.UnknownCheckError{Kind: model.Kind(draft.Kind), Check: draft.Check}).Error()
	}
	if draft.HeadingIndex < 0 || draft.HeadingIndex >= len(request.Headings) {
		return "", 0, "", fmt.Sprintf("heading %d does not exist in this document", draft.HeadingIndex)
	}
	anchor := request.Headings[draft.HeadingIndex]
	return render(draft), anchor.endLine, anchor.Text, ""
}

// render assembles the fenced block. The shape is fixed here rather than taken
// from the model, so a drafted assertion always looks like a hand-written one.
func render(draft Draft) string {
	var b strings.Builder
	b.WriteString("```sonde\n")
	fmt.Fprintf(&b, "id: %s\n", draft.ID)
	fmt.Fprintf(&b, "kind: %s\n", draft.Kind)
	fmt.Fprintf(&b, "check: %s\n", draft.Check)
	for _, line := range strings.Split(strings.TrimRight(draft.Fields, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// parses runs the real parser over the document with this block appended. It is
// the only judge of whether a suggestion is valid: the parser is the contract,
// and a second opinion here would be a second contract.
func parses(request Request, source []byte, block string) string {
	candidate := append(append([]byte{}, source...), []byte("\n"+block)...)
	if request.Meta.ID == "" {
		// The document has no frontmatter, so the parser would skip it and
		// tell us nothing. Wrap it in the minimum that makes it a runbook,
		// purely to check the block.
		candidate = append([]byte("---\nsonde:\n  version: 1\n  id: suggest-probe\n---\n"), candidate...)
	}
	runbook, err := parse.Source(request.Path, candidate)
	if err != nil {
		return firstLine(err.Error())
	}
	if runbook == nil {
		return "the document is not a runbook"
	}
	return ""
}

func firstLine(message string) string {
	if index := strings.IndexByte(message, '\n'); index >= 0 {
		message = message[:index]
	}
	// The parser prefixes errors with path:line, which means nothing here
	// because the line is in a document that only existed for the check.
	if _, rest, found := strings.Cut(message, ": "); found {
		return rest
	}
	return message
}
