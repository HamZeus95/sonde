package suggest

import (
	"fmt"
	"strings"
)

// contextLines is how much of the surrounding document a hunk shows. Three is
// what git uses, and a reviewer reading a suggested assertion needs to see the
// step it was placed under.
const contextLines = 3

// Apply inserts the suggested blocks into a document.
//
// Bottom-up, so that inserting one block cannot move the line another was
// anchored to.
func Apply(source []byte, suggestions []Suggestion) []byte {
	lines := splitLines(source)
	for i := len(suggestions) - 1; i >= 0; i-- {
		suggestion := suggestions[i]
		at := clamp(suggestion.AfterLine, 0, len(lines))
		inserted := blockLines(suggestion.Block)
		rest := append([]string{}, lines[at:]...)
		lines = append(lines[:at], append(inserted, rest...)...)
	}
	return []byte(strings.Join(lines, "\n"))
}

// Diff renders the suggestions as a unified diff.
//
// A diff rather than a rewritten file, because the human is the approver here:
// the model drafts, and what lands in the repository is what someone read and
// chose to keep.
func Diff(path string, source []byte, suggestions []Suggestion) string {
	if len(suggestions) == 0 {
		return ""
	}
	lines := splitLines(source)

	// Insertions close together share a hunk, so a reviewer is not shown the
	// same three lines of context twice.
	groups := [][]Suggestion{}
	for _, suggestion := range suggestions {
		last := len(groups) - 1
		if last >= 0 {
			previous := groups[last][len(groups[last])-1]
			if suggestion.AfterLine-previous.AfterLine <= contextLines*2 {
				groups[last] = append(groups[last], suggestion)
				continue
			}
		}
		groups = append(groups, []Suggestion{suggestion})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", path, path)
	offset := 0
	for _, group := range groups {
		first := clamp(group[0].AfterLine, 0, len(lines))
		last := clamp(group[len(group)-1].AfterLine, 0, len(lines))

		oldStart := max(1, first-contextLines+1)
		oldEnd := min(len(lines), last+contextLines)
		added := 0
		for _, suggestion := range group {
			added += len(blockLines(suggestion.Block))
		}
		oldCount := oldEnd - oldStart + 1
		if oldCount < 0 {
			oldCount = 0
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, oldStart+offset, oldCount+added)

		// An insertion anchored above the first line has nothing to follow.
		for _, suggestion := range group {
			if suggestion.AfterLine <= 0 {
				writeAdded(&b, suggestion)
			}
		}
		for line := oldStart; line <= oldEnd; line++ {
			fmt.Fprintf(&b, " %s\n", lines[line-1])
			for _, suggestion := range group {
				if suggestion.AfterLine == line {
					writeAdded(&b, suggestion)
				}
			}
		}
		offset += added
	}
	return b.String()
}

func writeAdded(b *strings.Builder, suggestion Suggestion) {
	for _, line := range blockLines(suggestion.Block) {
		fmt.Fprintf(b, "+%s\n", line)
	}
}

// blockLines renders a block as the lines a diff adds: a blank line to separate
// it from the prose above, then the fence.
func blockLines(block string) []string {
	lines := []string{""}
	return append(lines, splitLines([]byte(strings.TrimRight(block, "\n")))...)
}

func splitLines(source []byte) []string {
	text := strings.ReplaceAll(string(source), "\r\n", "\n")
	return strings.Split(text, "\n")
}

func clamp(value, low, high int) int {
	return max(low, min(value, high))
}
