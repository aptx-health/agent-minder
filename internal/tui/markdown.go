package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// renderMarkdown converts markdown text to styled terminal output using
// lipgloss and the current theme. Handles headers, bold, italic, inline code,
// code blocks, bullet lists, and tables.
func renderMarkdown(md string, width int) string {
	theme := currentTheme()

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	h2Style := lipgloss.NewStyle().Bold(true).Foreground(theme.Secondary)
	h3Style := lipgloss.NewStyle().Bold(true).Foreground(theme.Secondary)
	boldStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Text)
	codeStyle := lipgloss.NewStyle().Foreground(theme.Success)
	bulletStyle := lipgloss.NewStyle().Foreground(theme.Warning)
	textStyle := lipgloss.NewStyle().Foreground(theme.LLMText)
	mutedStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	tableHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(theme.Primary)
	tableBorderStyle := lipgloss.NewStyle().Foreground(theme.Muted)
	tableCellStyle := lipgloss.NewStyle().Foreground(theme.LLMText)

	lines := strings.Split(md, "\n")
	var out []string
	inCodeBlock := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Code blocks.
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			out = append(out, "  "+codeStyle.Render(line))
			continue
		}

		// Tables: detect a pipe-delimited line.
		if isTableRow(trimmed) {
			tableLines := []string{trimmed}
			for i+1 < len(lines) && isTableRow(strings.TrimSpace(lines[i+1])) {
				i++
				tableLines = append(tableLines, strings.TrimSpace(lines[i]))
			}
			out = append(out, renderTable(tableLines, tableHeaderStyle, tableBorderStyle, tableCellStyle, boldStyle, codeStyle, textStyle)...)
			continue
		}

		// Headers.
		if strings.HasPrefix(trimmed, "### ") {
			out = append(out, "  "+h3Style.Render(strings.TrimPrefix(trimmed, "### ")))
			continue
		}
		if strings.HasPrefix(trimmed, "## ") {
			out = append(out, "  "+h2Style.Render(strings.TrimPrefix(trimmed, "## ")))
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			out = append(out, "  "+headerStyle.Render(strings.TrimPrefix(trimmed, "# ")))
			continue
		}

		// Horizontal rule.
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			out = append(out, "  "+mutedStyle.Render(strings.Repeat("─", min(width-4, 40))))
			continue
		}

		// Bullet lists.
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			content := applyInlineStyles(trimmed[2:], boldStyle, codeStyle, textStyle)
			out = append(out, "  "+bulletStyle.Render("•")+" "+content)
			continue
		}

		// Numbered lists.
		if numListRe.MatchString(trimmed) {
			idx := strings.Index(trimmed, " ")
			num := trimmed[:idx]
			content := applyInlineStyles(trimmed[idx+1:], boldStyle, codeStyle, textStyle)
			out = append(out, "  "+bulletStyle.Render(num)+" "+content)
			continue
		}

		// Empty lines.
		if trimmed == "" {
			out = append(out, "")
			continue
		}

		// Normal text with inline formatting.
		out = append(out, "  "+applyInlineStyles(trimmed, boldStyle, codeStyle, textStyle))
	}

	return strings.Join(out, "\n")
}

var (
	boldRe    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	codeRe    = regexp.MustCompile("`([^`]+)`")
	numListRe = regexp.MustCompile(`^\d+\.\s`)
)

// applyInlineStyles handles **bold** and `code` within a line.
func applyInlineStyles(s string, boldStyle, codeStyle, textStyle lipgloss.Style) string {
	// Process inline code first (so bold/italic inside code isn't styled).
	s = codeRe.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[1 : len(match)-1]
		return codeStyle.Render(inner)
	})

	// Bold.
	s = boldRe.ReplaceAllStringFunc(s, func(match string) string {
		inner := match[2 : len(match)-2]
		return boldStyle.Render(inner)
	})

	return s
}

// isTableRow returns true if the line looks like a markdown table row (has pipes).
func isTableRow(line string) bool {
	return strings.Contains(line, "|") && strings.Count(line, "|") >= 2
}

// isSeparatorRow returns true if the row is a table separator (e.g., |---|---|).
func isSeparatorRow(line string) bool {
	cells := splitTableRow(line)
	for _, c := range cells {
		cleaned := strings.TrimSpace(c)
		cleaned = strings.TrimLeft(cleaned, ":")
		cleaned = strings.TrimRight(cleaned, ":")
		if cleaned == "" || strings.Trim(cleaned, "-") != "" {
			return false
		}
	}
	return len(cells) > 0
}

// splitTableRow splits a pipe-delimited row into cells.
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// renderTable renders a collected set of markdown table lines.
func renderTable(tableLines []string, headerStyle, borderStyle, cellStyle, boldStyle, codeStyle, textStyle lipgloss.Style) []string {
	if len(tableLines) == 0 {
		return nil
	}

	// Parse all rows, skipping separator rows.
	var rows [][]string
	headerIdx := -1
	for i, line := range tableLines {
		if isSeparatorRow(line) {
			// The row before the separator is the header.
			if i > 0 && headerIdx == -1 {
				headerIdx = i - 1
			}
			continue
		}
		rows = append(rows, splitTableRow(line))
	}

	if len(rows) == 0 {
		return nil
	}

	// If we didn't find a separator, treat first row as header anyway.
	actualHeaderRow := 0
	if headerIdx >= 0 {
		actualHeaderRow = 0 // First non-separator row is header.
	}

	// Determine column count and widths.
	numCols := 0
	for _, row := range rows {
		if len(row) > numCols {
			numCols = len(row)
		}
	}

	colWidths := make([]int, numCols)
	for _, row := range rows {
		for c := 0; c < len(row) && c < numCols; c++ {
			w := utf8.RuneCountInString(row[c])
			if w > colWidths[c] {
				colWidths[c] = w
			}
		}
	}

	// Render rows.
	var out []string

	// Top border.
	out = append(out, "  "+borderStyle.Render(tableBorder(colWidths, "┌", "┬", "┐")))

	for r, row := range rows {
		var cells []string
		for c := 0; c < numCols; c++ {
			val := ""
			if c < len(row) {
				val = row[c]
			}
			padded := val + strings.Repeat(" ", colWidths[c]-utf8.RuneCountInString(val))
			if r == actualHeaderRow {
				cells = append(cells, headerStyle.Render(padded))
			} else {
				cells = append(cells, applyInlineStyles(padded, boldStyle, codeStyle, cellStyle))
			}
		}
		out = append(out, "  "+borderStyle.Render("│")+" "+strings.Join(cells, " "+borderStyle.Render("│")+" ")+" "+borderStyle.Render("│"))

		// Separator after header.
		if r == actualHeaderRow {
			out = append(out, "  "+borderStyle.Render(tableBorder(colWidths, "├", "┼", "┤")))
		}
	}

	// Bottom border.
	out = append(out, "  "+borderStyle.Render(tableBorder(colWidths, "└", "┴", "┘")))

	return out
}

// tableBorder builds a horizontal border line like ┌───┬───┐.
func tableBorder(colWidths []int, left, mid, right string) string {
	var parts []string
	for _, w := range colWidths {
		parts = append(parts, strings.Repeat("─", w+2))
	}
	return left + strings.Join(parts, mid) + right
}
