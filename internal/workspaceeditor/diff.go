package workspaceeditor

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

type textLine struct {
	value      string
	hasNewline bool
}

func unifiedFileDiff(path string, oldContent []byte, oldExists bool, newContent []byte, newExists bool) (string, error) {
	oldLines := splitTextLines(oldContent)
	newLines := splitTextLines(newContent)
	groups := difflib.NewMatcher(lineValues(oldLines), lineValues(newLines)).GetGroupedOpCodes(3)
	if len(groups) == 0 {
		return "", nil
	}
	fromFile, toFile := "a/"+path, "b/"+path
	if !oldExists {
		fromFile = "/dev/null"
	}
	if !newExists {
		toFile = "/dev/null"
	}
	var output strings.Builder
	fmt.Fprintf(&output, "diff --git a/%s b/%s\n", path, path)
	if !oldExists {
		output.WriteString("new file mode 100644\n")
	}
	if !newExists {
		output.WriteString("deleted file mode 100644\n")
	}
	fmt.Fprintf(&output, "--- %s\n+++ %s\n", fromFile, toFile)
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&output, "@@ -%s +%s @@\n", unifiedRange(first.I1, last.I2), unifiedRange(first.J1, last.J2))
		for _, operation := range group {
			switch operation.Tag {
			case 'e':
				writeLines(&output, ' ', oldLines[operation.I1:operation.I2])
			case 'r':
				writeLines(&output, '-', oldLines[operation.I1:operation.I2])
				writeLines(&output, '+', newLines[operation.J1:operation.J2])
			case 'd':
				writeLines(&output, '-', oldLines[operation.I1:operation.I2])
			case 'i':
				writeLines(&output, '+', newLines[operation.J1:operation.J2])
			default:
				return "", fmt.Errorf("unsupported diff operation %q", operation.Tag)
			}
		}
	}
	return output.String(), nil
}

func splitTextLines(content []byte) []textLine {
	if len(content) == 0 {
		return nil
	}
	parts := strings.SplitAfter(string(content), "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	lines := make([]textLine, 0, len(parts))
	for _, part := range parts {
		hasNewline := strings.HasSuffix(part, "\n")
		lines = append(lines, textLine{value: strings.TrimSuffix(part, "\n"), hasNewline: hasNewline})
	}
	return lines
}

func lineValues(lines []textLine) []string {
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = line.value
		if line.hasNewline {
			values[index] += "\n"
		}
	}
	return values
}

func writeLines(output *strings.Builder, prefix byte, lines []textLine) {
	for _, line := range lines {
		output.WriteByte(prefix)
		output.WriteString(line.value)
		output.WriteByte('\n')
		if !line.hasNewline {
			output.WriteString("\\ No newline at end of file\n")
		}
	}
}

func unifiedRange(start, stop int) string {
	beginning, length := start+1, stop-start
	if length == 0 {
		beginning--
	}
	if length == 1 {
		return fmt.Sprint(beginning)
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}
