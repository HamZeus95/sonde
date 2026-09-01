package parse

import (
	"bytes"
	"fmt"

	"sigs.k8s.io/yaml"

	"github.com/HamZeus95/sonde/internal/model"
)

// frontmatterDelim opens and closes a YAML frontmatter block.
var frontmatterDelim = []byte("---")

// splitFrontmatter returns the YAML frontmatter of src, if any. found is false
// when the document does not open with a frontmatter block at all, which is the
// common case for the ninety percent of a repository's Markdown that has nothing
// to do with Sonde.
func splitFrontmatter(src []byte) (fm []byte, found bool) {
	rest, ok := bytes.CutPrefix(src, frontmatterDelim)
	if !ok {
		return nil, false
	}
	// The opening delimiter must be alone on its line, so what follows it is a
	// line break and nothing else. "---8<---" is a horizontal rule, not
	// frontmatter.
	rest, ok = cutLineBreak(rest)
	if !ok {
		return nil, false
	}
	for offset := 0; offset < len(rest); {
		line, next := lineAt(rest, offset)
		if bytes.Equal(bytes.TrimRight(line, " \t"), frontmatterDelim) {
			return rest[:offset], true
		}
		offset = next
	}
	// Unterminated frontmatter: the whole file is one open block. Treat it as
	// absent rather than guessing where it ends.
	return nil, false
}

func cutLineBreak(b []byte) ([]byte, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("\r\n")):
		return b[2:], true
	case bytes.HasPrefix(b, []byte("\n")):
		return b[1:], true
	case len(b) == 0:
		return b, true
	default:
		return b, false
	}
}

func lineAt(b []byte, offset int) (line []byte, next int) {
	end := bytes.IndexByte(b[offset:], '\n')
	if end < 0 {
		return b[offset:], len(b)
	}
	line = b[offset : offset+end]
	return bytes.TrimSuffix(line, []byte("\r")), offset + end + 1
}

// frontmatter is the shape we read out of a runbook's frontmatter. Everything
// else in there — title, tags, whatever the team's docs tooling wants — is none
// of our business and is left alone.
type frontmatter struct {
	Sonde *model.Meta `json:"sonde"`
}

// metaFields are the keys accepted under `sonde:`, used to warn about typos.
var metaFields = []string{"version", "id", "owner", "environment", "criticality"}

// MetaInfoString marks a fenced block carrying a runbook's metadata, for
// sources that have no frontmatter to put it in.
//
// Confluence and Notion pages are not files: there is nowhere to write a YAML
// header. Rather than have their connectors invent an id from a page title that
// someone will rename, a page declares itself the same way it declares its
// assertions — in a block.
const MetaInfoString = "sonde-runbook"

// parseFrontmatter reads the `sonde:` block. It returns isRunbook false when the
// document has frontmatter but no sonde key: that is an ordinary Markdown file
// and must be skipped silently, with no warning, or every repository that
// adopts Sonde drowns in noise on its first run.
func parseFrontmatter(fm []byte) (meta model.Meta, isRunbook bool, warnings []model.Warning, err error) {
	var probe map[string]any
	if err := yaml.Unmarshal(fm, &probe); err != nil {
		// Malformed frontmatter in a file that may not even be a runbook is
		// not our problem to report.
		return model.Meta{}, false, nil, nil
	}
	raw, ok := probe["sonde"]
	if !ok {
		return model.Meta{}, false, nil, nil
	}
	sondeKeys, ok := raw.(map[string]any)
	if !ok {
		return model.Meta{}, true, nil, fmt.Errorf("frontmatter key sonde must be a mapping")
	}

	var parsed frontmatter
	if err := yaml.Unmarshal(fm, &parsed); err != nil {
		return model.Meta{}, true, nil, fmt.Errorf("read frontmatter: %w", err)
	}
	meta = *parsed.Sonde

	for _, key := range unknownKeys(sondeKeys, metaFields) {
		warnings = append(warnings, model.Warning{
			Line:    frontmatterLine,
			Message: fmt.Sprintf("unknown frontmatter field %q under sonde", key),
		})
	}

	if err := validateMeta(meta, "frontmatter sonde."); err != nil {
		return meta, true, warnings, err
	}
	return meta, true, warnings, nil
}

// parseMetaBlock reads a `sonde-runbook` block as a runbook's metadata.
//
// The body is the same mapping that would sit under `sonde:` in frontmatter,
// without the extra nesting: a page writes `version: 1` and `id: ...` directly.
func parseMetaBlock(body []byte, line int) (meta model.Meta, warnings []model.Warning, err error) {
	var keys map[string]any
	if err := yaml.Unmarshal(body, &keys); err != nil {
		return model.Meta{}, nil, fmt.Errorf("read %s block: %s", MetaInfoString, cleanYAMLError(err))
	}
	if len(keys) == 0 {
		return model.Meta{}, nil, fmt.Errorf("%s block is empty: it needs at least version and id", MetaInfoString)
	}
	if err := yaml.Unmarshal(body, &meta); err != nil {
		return model.Meta{}, nil, fmt.Errorf("read %s block: %s", MetaInfoString, cleanYAMLError(err))
	}
	for _, key := range unknownKeys(keys, metaFields) {
		warnings = append(warnings, model.Warning{
			Line:    line,
			Message: fmt.Sprintf("unknown field %q in the %s block", key, MetaInfoString),
		})
	}
	if err := validateMeta(meta, ""); err != nil {
		return meta, warnings, err
	}
	return meta, warnings, nil
}

// validateMeta is the shared check for a runbook's metadata, however it
// arrived. `field` prefixes the names in error messages so that the same rule
// reads correctly whether it was broken in frontmatter or in a block.
func validateMeta(meta model.Meta, field string) error {
	if meta.Version != model.ContractVersion {
		if meta.Version == 0 {
			return fmt.Errorf("%sversion is required and must be %d", field, model.ContractVersion)
		}
		return fmt.Errorf("runbook format version %d is not supported by this build (expected %d)", meta.Version, model.ContractVersion)
	}
	if meta.ID == "" {
		return fmt.Errorf("%sid is required", field)
	}
	if err := validateSlug(field+"id", meta.ID); err != nil {
		return err
	}
	if meta.Criticality != "" && !meta.Criticality.Valid() {
		return fmt.Errorf("criticality %q is not one of high, medium, low", meta.Criticality)
	}
	return nil
}

// frontmatterLine is the line reported for frontmatter problems. Frontmatter
// starts at line 1 by definition, and pointing at the block is more useful than
// pointing at a key inside a document the user is already looking at.
const frontmatterLine = 1
