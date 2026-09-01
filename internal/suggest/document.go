package suggest

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/HamZeus95/sonde/internal/parse"
)

// headingsOf lists a document's headings and the extent of each one's section.
//
// A section ends where the next heading of the same or higher level begins, so
// a block anchored to "Step 2" lands inside step 2 and not after the whole
// document.
func headingsOf(source []byte) ([]Heading, error) {
	lines := lineOffsets(source)
	// Frontmatter is not Markdown, but CommonMark does not know that: its
	// closing `---` closes a setext heading over the YAML above it, which
	// arrives here as a heading called "sonde:". Everything at or above the
	// closing delimiter is skipped.
	frontmatterEnd := frontmatterEndLine(source)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))

	var headings []Heading
	err := ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		if heading.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		segment := heading.Lines().At(0)
		line := lineAt(lines, segment.Start)
		if line <= frontmatterEnd {
			return ast.WalkContinue, nil
		}
		headings = append(headings, Heading{
			Index: len(headings),
			Level: heading.Level,
			Text:  strings.TrimSpace(string(segment.Value(source))),
			line:  line,
		})
		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, err
	}

	total := len(lines)
	for i := range headings {
		headings[i].endLine = total
		for j := i + 1; j < len(headings); j++ {
			if headings[j].Level <= headings[i].Level {
				headings[i].endLine = headings[j].line - 1
				break
			}
		}
		// Trailing blank lines belong to the gap between sections, not to the
		// section, so a block inserted here sits directly under the prose.
		for headings[i].endLine > headings[i].line &&
			strings.TrimSpace(lineText(source, lines, headings[i].endLine)) == "" {
			headings[i].endLine--
		}
	}
	return headings, nil
}

// frontmatterEndLine returns the 1-indexed line of the frontmatter's closing
// delimiter, or 0 when the document has no frontmatter.
func frontmatterEndLine(source []byte) int {
	lines := bytes.Split(source, []byte("\n"))
	if len(lines) == 0 || strings.TrimRight(string(lines[0]), " \t\r") != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(string(lines[i]), " \t\r") == "---" {
			return i + 1
		}
	}
	// Unterminated: the parser treats the document as having no frontmatter,
	// and so does this.
	return 0
}

// stripBlocks removes existing sonde blocks from the prose handed to the model.
//
// The model should draft from the steps, not from the answers: leaving the
// existing assertions in invites it to produce variations of them instead of
// looking at what is undocumented.
func stripBlocks(source []byte) []byte {
	lines := bytes.Split(source, []byte("\n"))
	var out [][]byte
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(string(line))
		switch {
		case inBlock:
			if trimmed == "```" {
				inBlock = false
			}
		case trimmed == "```"+parse.InfoString || trimmed == "```"+parse.IgnoreInfoString:
			inBlock = true
		default:
			out = append(out, line)
		}
	}
	return bytes.Join(out, []byte("\n"))
}

// lineOffsets records the byte offset each line starts at.
func lineOffsets(source []byte) []int {
	offsets := []int{0}
	for i, b := range source {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

// lineAt converts a byte offset to a 1-indexed line.
func lineAt(offsets []int, offset int) int {
	line := 1
	for i, start := range offsets {
		if start > offset {
			break
		}
		line = i + 1
	}
	return line
}

// lineText returns one 1-indexed line, without its terminator.
func lineText(source []byte, offsets []int, line int) string {
	if line < 1 || line > len(offsets) {
		return ""
	}
	start := offsets[line-1]
	end := len(source)
	if line < len(offsets) {
		end = offsets[line] - 1
	}
	if start > end {
		return ""
	}
	return string(source[start:end])
}
