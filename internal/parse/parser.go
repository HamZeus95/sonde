// Package parse turns a Markdown runbook into a Runbook and its Checks.
//
// It is the only parser in the product. The control plane consumes this
// package's JSON output rather than reading Markdown itself; a second parser in
// another language would drift from this one within a month and the two would
// disagree about what a customer's runbook says.
//
// Parsing never executes anything: no commands, no template expansion, no
// network. `sonde parse` is safe to point at untrusted input.
package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"sigs.k8s.io/yaml"

	"github.com/HamZeus95/sonde/internal/canonical"
	"github.com/HamZeus95/sonde/internal/model"
)

// InfoString marks a fenced block as an assertion. It must match exactly:
// anything else is a code block that happens to contain YAML.
const InfoString = "sonde"

// IgnoreInfoString marks a block that looks like an assertion but is not one —
// an example inside the documentation of Sonde itself, most often.
const IgnoreInfoString = "sonde-ignore"

// Error is one problem with one runbook, located precisely enough to click.
type Error struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Error implements error.
func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", e.Path, e.Line, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// Errors is every problem found, rather than the first. A user fixing a runbook
// wants the whole list in one pass.
type Errors []*Error

// Error implements error.
func (e Errors) Error() string {
	switch len(e) {
	case 0:
		return "no parse errors"
	case 1:
		return e[0].Error()
	default:
		lines := make([]string, len(e))
		for i, err := range e {
			lines[i] = err.Error()
		}
		return strings.Join(lines, "\n")
	}
}

// slugPattern constrains ids: they end up in URLs, JUnit test names and Slack
// messages, so they may not contain whitespace or punctuation that needs
// escaping in any of those.
var slugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateSlug(field, value string) error {
	if !slugPattern.MatchString(value) {
		return fmt.Errorf("%s %q must start with a letter or digit and contain only letters, digits, dot, dash or underscore", field, value)
	}
	return nil
}

// Source parses one runbook from bytes already read.
//
// It returns a nil Runbook and no error when src is not a Sonde runbook: a
// Markdown file with no sonde frontmatter key is skipped silently.
func Source(path string, src []byte) (*model.Runbook, error) {
	path = filepath.ToSlash(path)
	blocks, metaBlocks, errs := collectBlocks(path, src)

	var (
		meta      model.Meta
		warnings  []model.Warning
		isRunbook bool
	)
	if fm, found := splitFrontmatter(src); found {
		var err error
		meta, isRunbook, warnings, err = parseFrontmatter(fm)
		if isRunbook && err != nil {
			return nil, Errors{{Path: path, Line: frontmatterLine, Message: err.Error()}}
		}
	}
	if !isRunbook {
		// No frontmatter, or frontmatter that says nothing about Sonde. A
		// document may still declare itself with a metadata block, which is how
		// a Confluence or Notion page — which has nowhere to put a YAML header
		// — becomes a runbook.
		switch len(metaBlocks) {
		case 0:
			return nil, nil
		case 1:
			var err error
			meta, warnings, err = parseMetaBlock(metaBlocks[0].body, metaBlocks[0].line)
			if err != nil {
				return nil, Errors{{Path: path, Line: metaBlocks[0].line, Message: err.Error()}}
			}
		default:
			return nil, Errors{{
				Path: path, Line: metaBlocks[1].line,
				Message: fmt.Sprintf("a document may have one %s block; this one has %d", MetaInfoString, len(metaBlocks)),
			}}
		}
	} else if len(metaBlocks) > 0 {
		// Both. The frontmatter is authoritative — it is the form the parser
		// has always used — and the block is reported rather than ignored.
		warnings = append(warnings, model.Warning{
			Line:    metaBlocks[0].line,
			Message: fmt.Sprintf("this document has both frontmatter and a %s block; the frontmatter is used", MetaInfoString),
		})
	}

	sum := sha256.Sum256(src)
	rb := &model.Runbook{
		Path:        path,
		ContentHash: hex.EncodeToString(sum[:]),
		Meta:        meta,
		Checks:      []model.Check{},
		Warnings:    warnings,
	}

	seen := make(map[string]int, len(blocks))
	for _, b := range blocks {
		check, blockWarnings, err := b.decode(meta)
		rb.Warnings = append(rb.Warnings, blockWarnings...)
		if err != nil {
			errs = append(errs, &Error{Path: path, Line: b.line, Message: err.Error()})
			continue
		}
		if first, dup := seen[check.ID]; dup {
			errs = append(errs, &Error{
				Path:    path,
				Line:    b.line,
				Message: fmt.Sprintf("duplicate check id %q, already defined on line %d", check.ID, first),
			})
			continue
		}
		seen[check.ID] = b.line
		rb.Checks = append(rb.Checks, check)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return rb, nil
}

// File reads and parses one runbook.
func File(path string) (*model.Runbook, error) {
	src, err := os.ReadFile(path) //nolint:gosec // reading the paths the user named is the whole job
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return Source(path, src)
}

// Walk parses every Markdown file under the given files and directories.
//
// Results are sorted by path so that two runs over the same tree produce
// byte-identical JSON, which is what makes golden files and content hashes
// worth having. Every file is parsed even after one fails: the caller gets the
// complete list of problems.
func Walk(roots []string) ([]model.Runbook, error) {
	var (
		runbooks []model.Runbook
		errs     Errors
	)
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", root, err)
		}
		if !info.IsDir() {
			rb, err := File(root)
			collect(&runbooks, &errs, rb, err)
			continue
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
			if !isMarkdown(path) {
				return nil
			}
			rb, parseErr := File(path)
			collect(&runbooks, &errs, rb, parseErr)
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk %s: %w", root, walkErr)
		}
	}
	sort.Slice(runbooks, func(i, j int) bool { return runbooks[i].Path < runbooks[j].Path })
	sort.Slice(errs, func(i, j int) bool {
		if errs[i].Path != errs[j].Path {
			return errs[i].Path < errs[j].Path
		}
		return errs[i].Line < errs[j].Line
	})
	if len(errs) > 0 {
		return runbooks, errs
	}
	return runbooks, nil
}

func collect(runbooks *[]model.Runbook, errs *Errors, rb *model.Runbook, err error) {
	var parseErrs Errors
	switch {
	case err == nil:
		if rb != nil {
			*runbooks = append(*runbooks, *rb)
		}
	case errors.As(err, &parseErrs):
		*errs = append(*errs, parseErrs...)
	default:
		*errs = append(*errs, &Error{Message: err.Error()})
	}
}

func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	default:
		return false
	}
}

// skipDir keeps the walker out of directories that never hold runbooks but do
// hold thousands of Markdown files.
func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".terraform":
		return true
	}
	return strings.HasPrefix(name, ".") && name != "."
}

// block is one fenced sonde block, located in its file.
type block struct {
	path string
	line int
	body []byte
}

func collectBlocks(path string, src []byte) (blocks, metaBlocks []block, errs Errors) {
	lines := newLineIndex(src)
	doc := goldmark.New().Parser().Parse(text.NewReader(src))
	// Walk cannot fail: the visitor below never returns an error.
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		fenced, ok := n.(*ast.FencedCodeBlock)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		if fenced.Info == nil {
			return ast.WalkContinue, nil
		}
		info := strings.TrimSpace(string(fenced.Info.Segment.Value(src)))
		line := lines.of(fenced.Info.Segment.Start)
		isMeta := info == MetaInfoString
		switch {
		case info == InfoString, isMeta:
		case info == IgnoreInfoString:
			return ast.WalkContinue, nil
		case strings.EqualFold(info, InfoString):
			errs = append(errs, &Error{
				Path:    path,
				Line:    line,
				Message: fmt.Sprintf("info string %q is not %q: the info string is case sensitive, so this block would be ignored", info, InfoString),
			})
			return ast.WalkContinue, nil
		default:
			return ast.WalkContinue, nil
		}

		var body []byte
		segments := fenced.Lines()
		for i := range segments.Len() {
			segment := segments.At(i)
			body = append(body, segment.Value(src)...)
		}
		found := block{path: path, line: line, body: body}
		if isMeta {
			metaBlocks = append(metaBlocks, found)
		} else {
			blocks = append(blocks, found)
		}
		return ast.WalkContinue, nil
	})
	return blocks, metaBlocks, errs
}

// blockCommon is the part of a block that every check shares.
type blockCommon struct {
	ID      string          `json:"id"`
	Kind    model.Kind      `json:"kind"`
	Check   string          `json:"check"`
	Enabled *bool           `json:"enabled"`
	Timeout *model.Duration `json:"timeout"`
	Verbose bool            `json:"verbose"`
}

func (b block) decode(meta model.Meta) (model.Check, []model.Warning, error) {
	var keys map[string]any
	if err := yaml.Unmarshal(b.body, &keys); err != nil {
		return model.Check{}, nil, fmt.Errorf("read sonde block: %s", cleanYAMLError(err))
	}
	if len(keys) == 0 {
		return model.Check{}, nil, fmt.Errorf("sonde block is empty: it needs at least id, kind and check")
	}

	var common blockCommon
	if err := yaml.Unmarshal(b.body, &common); err != nil {
		return model.Check{}, nil, fmt.Errorf("read sonde block: %s", cleanYAMLError(err))
	}
	if common.ID == "" {
		return model.Check{}, nil, fmt.Errorf("id is required")
	}
	if err := validateSlug("id", common.ID); err != nil {
		return model.Check{}, nil, err
	}
	if common.Kind == "" {
		return model.Check{}, nil, fmt.Errorf("kind is required: valid kinds are %s", model.DescribeKinds())
	}
	if common.Check == "" {
		return model.Check{}, nil, fmt.Errorf("check is required: valid checks for kind %q are %s",
			common.Kind, strings.Join(model.ChecksForKind(common.Kind), ", "))
	}
	def, ok := model.Lookup(common.Kind, common.Check)
	if !ok {
		return model.Check{}, nil, &model.UnknownCheckError{Kind: common.Kind, Check: common.Check}
	}

	spec := def.New()
	if err := yaml.Unmarshal(b.body, spec); err != nil {
		return model.Check{}, nil, fmt.Errorf("read %s/%s fields: %s", common.Kind, common.Check, cleanYAMLError(err))
	}
	if err := spec.Validate(); err != nil {
		return model.Check{}, nil, fmt.Errorf("%s/%s: %w", common.Kind, common.Check, err)
	}

	check := model.Check{
		ID:      common.ID,
		Kind:    common.Kind,
		Check:   common.Check,
		Spec:    spec,
		Line:    b.line,
		Enabled: common.Enabled == nil || *common.Enabled,
		Timeout: common.Timeout,
		Verbose: common.Verbose,
	}

	// Unknown fields are a warning, never an error: a runbook written against a
	// newer Sonde must still run against an older one.
	var warnings []model.Warning
	for _, key := range unknownKeys(keys, model.AcceptedFields(spec)) {
		warnings = append(warnings, model.Warning{
			Line:    b.line,
			CheckID: check.ID,
			Message: fmt.Sprintf("unknown field %q for %s/%s", key, check.Kind, check.Check),
		})
	}

	uri, err := canonical.ForCheck(meta, check)
	switch {
	case err == nil:
		check.Canonical = uri
	case errors.Is(err, canonical.ErrNotIndexable):
		// Expected for checks that name no single resource.
	case errors.Is(err, canonical.ErrNoCluster), errors.Is(err, canonical.ErrNoRealm):
		warnings = append(warnings, model.Warning{
			Line:    b.line,
			CheckID: check.ID,
			Message: fmt.Sprintf("check will run but will not appear in the reverse index: %s", err),
		})
	default:
		return model.Check{}, warnings, err
	}
	return check, warnings, nil
}

// yamlNoise are the layers sigs.k8s.io/yaml wraps around a decoding failure on
// its way through JSON. They describe our decoding pipeline, not the user's
// mistake, and a parse error should read like a compiler's, not like a stack.
var yamlNoise = []string{
	"error unmarshaling JSON: ",
	"error converting YAML to JSON: ",
	"while decoding JSON: ",
	"json: ",
}

func cleanYAMLError(err error) string {
	msg := err.Error()
	for _, noise := range yamlNoise {
		msg = strings.ReplaceAll(msg, noise, "")
	}
	return msg
}

// unknownKeys returns the keys of got that are not in accepted, sorted.
func unknownKeys(got map[string]any, accepted []string) []string {
	allowed := make(map[string]bool, len(accepted))
	for _, name := range accepted {
		allowed[name] = true
	}
	var unknown []string
	for key := range got {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// lineIndex converts byte offsets to 1-indexed line numbers.
type lineIndex struct {
	starts []int
}

func newLineIndex(src []byte) lineIndex {
	starts := []int{0}
	for i, b := range src {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return lineIndex{starts: starts}
}

func (l lineIndex) of(offset int) int {
	i := sort.SearchInts(l.starts, offset+1)
	if i < 1 {
		return 1
	}
	return i
}
