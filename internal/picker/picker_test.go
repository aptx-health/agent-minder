package picker

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/aptx-health/agent-minder/internal/daemon"
	"github.com/aptx-health/agent-minder/internal/db"
)

func TestFilterJobs(t *testing.T) {
	jobs := []*db.Job{
		{Agent: "spike", Name: "spike-issue-518", IssueNumber: 518, IssueTitle: sql.NullString{String: "[Feature] It'd be great if the...", Valid: true}, Status: "done", CostUSD: 0.88},
		{Agent: "ux-autopilot", Name: "ux-autopilot-issue-529", IssueNumber: 529, IssueTitle: sql.NullString{String: "Cleanup: remove old tour system", Valid: true}, Status: "reviewed", PRNumber: sql.NullInt64{Int64: 532, Valid: true}, CostUSD: 2.01},
		{Agent: "bug-fixer", Name: "bug-fixer-issue-521", IssueNumber: 521, IssueTitle: sql.NullString{String: "[Bug] Loading frog icon shows up", Valid: true}, Status: "reviewed", PRNumber: sql.NullInt64{Int64: 523, Valid: true}, CostUSD: 0.44},
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"empty filter returns all", "", 3},
		{"filter by agent name", "spike", 1},
		{"filter by issue number", "529", 1},
		{"filter by PR number", "532", 1},
		{"filter by title substring", "frog", 1},
		{"filter by status", "done", 1},
		{"case insensitive", "BUG-FIXER", 1},
		{"no match", "nonexistent", 0},
		{"partial agent match", "auto", 1},
		{"multiple matches", "reviewed", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterJobs(jobs, tt.filter)
			if len(got) != tt.want {
				t.Errorf("FilterJobs(%q) returned %d jobs, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestFilterRemoteJobs(t *testing.T) {
	jobs := []daemon.JobResponse{
		{Agent: "spike", Name: "spike-issue-518", IssueNumber: 518, Title: "[Feature] It'd be great if the...", Status: "done", CostUSD: 0.88},
		{Agent: "ux-autopilot", Name: "ux-autopilot-issue-529", IssueNumber: 529, Title: "Cleanup: remove old tour system", Status: "reviewed", PRNumber: 532, CostUSD: 2.01},
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"empty filter returns all", "", 2},
		{"filter by agent", "spike", 1},
		{"filter by PR number", "532", 1},
		{"no match", "xyz", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterRemoteJobs(jobs, tt.filter)
			if len(got) != tt.want {
				t.Errorf("FilterRemoteJobs(%q) returned %d jobs, want %d", tt.filter, len(got), tt.want)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		s    string
		w    int
		want string
	}{
		{"abc", 6, "abc   "},
		{"abcdef", 6, "abcdef"},
		{"abcdefgh", 6, "abc..."},
		{"a", 2, "a "},
		{"", 4, "    "},
		{"abcdef", 3, "abc"},
		{"abcdef", 0, ""},
	}
	for _, tt := range tests {
		got := padRight(tt.s, tt.w)
		if got != tt.want {
			t.Errorf("padRight(%q, %d) = %q, want %q", tt.s, tt.w, got, tt.want)
		}
		if tt.w > 0 && len(got) != tt.w {
			t.Errorf("padRight(%q, %d) length %d, want %d", tt.s, tt.w, len(got), tt.w)
		}
	}
}

func TestPadLeft(t *testing.T) {
	tests := []struct {
		s    string
		w    int
		want string
	}{
		{"$1.45", 8, "   $1.45"},
		{"$1234.56", 8, "$1234.56"},
		{"$12345.67", 8, "$1234..."},
		{"", 4, "    "},
	}
	for _, tt := range tests {
		got := padLeft(tt.s, tt.w)
		if got != tt.want {
			t.Errorf("padLeft(%q, %d) = %q, want %q", tt.s, tt.w, got, tt.want)
		}
		if tt.w > 0 && len(got) != tt.w {
			t.Errorf("padLeft(%q, %d) length %d, want %d", tt.s, tt.w, len(got), tt.w)
		}
	}
}

func TestTitleWidthFor(t *testing.T) {
	tests := []struct {
		term int
		want int
	}{
		{200, 200 - fixedColumnWidth - listChromeWidth},
		{120, 120 - fixedColumnWidth - listChromeWidth},
		{60, minTitleWidth}, // too narrow → clamped
		{0, minTitleWidth},
	}
	for _, tt := range tests {
		got := titleWidthFor(tt.term)
		if got != tt.want {
			t.Errorf("titleWidthFor(%d) = %d, want %d", tt.term, got, tt.want)
		}
	}
}

// TestFormatJobLineColumns is the core acceptance test for the picker
// column formatting: ensures cost is shown in full, status/pr/cost align
// across rows, and agent/title use fixed/flexible widths.
func TestFormatJobLineColumns(t *testing.T) {
	titleWidth := 35
	jobs := []*db.Job{
		// Cheap, no PR, short agent.
		{
			Agent:       "spike",
			Name:        "spike-issue-518",
			IssueNumber: 518,
			IssueTitle:  sql.NullString{String: "Investigate weird thing", Valid: true},
			Status:      "done",
			CostUSD:     0.88,
		},
		// Bigger cost, with PR, mid-length agent.
		{
			Agent:       "ux-autopilot",
			Name:        "ux-autopilot-issue-529",
			IssueNumber: 529,
			IssueTitle:  sql.NullString{String: "Cleanup: remove old tour system", Valid: true},
			Status:      "reviewed",
			PRNumber:    sql.NullInt64{Int64: 532, Valid: true},
			CostUSD:     2.01,
		},
		// 4-digit issue, dollar value > $1, with PR.
		{
			Agent:       "autopilot",
			Name:        "autopilot-issue-1234",
			IssueNumber: 1234,
			IssueTitle:  sql.NullString{String: "Polish minder checkout column display", Valid: true},
			Status:      "reviewing",
			PRNumber:    sql.NullInt64{Int64: 1240, Valid: true},
			CostUSD:     1.45,
		},
		// Longest built-in agent.
		{
			Agent:       "dependency-updater",
			Name:        "dependency-updater-issue-300",
			IssueNumber: 300,
			IssueTitle:  sql.NullString{String: "Bump go-github", Valid: true},
			Status:      "queued",
			CostUSD:     0,
		},
		// Overflow agent (scheduled job style) — should be truncated.
		{
			Agent:       "monthly-lesson-groomer",
			Name:        "monthly-groom",
			IssueNumber: 0,
			Status:      "queued",
		},
		// Big cost, large PR number.
		{
			Agent:       "bug-fixer",
			Name:        "bug-fixer-issue-9999",
			IssueNumber: 9999,
			IssueTitle:  sql.NullString{String: "[Bug] something exploded", Valid: true},
			Status:      "bailed",
			PRNumber:    sql.NullInt64{Int64: 9876, Valid: true},
			CostUSD:     12.34,
		},
	}

	lines := make([]string, len(jobs))
	for i, j := range jobs {
		lines[i] = formatJobLine(j, titleWidth)
	}

	// Layout: label(6)  [agent(9)]  title(N)  status(10)  pr(7)  cost(8)
	// Derived offsets:
	agentBracketOff := labelWidth + 2          // 8
	agentStart := agentBracketOff + 1          // 9
	agentCloseOff := agentStart + agentWidth   // 18
	titleStart := agentCloseOff + 1 + 2        // 21
	statusStart := titleStart + titleWidth + 2 // 58
	prStart := statusStart + statusWidth + 2   // 70
	costStart := prStart + prWidth + 2         // 79

	expectedLen := fixedColumnWidth + titleWidth
	for i, line := range lines {
		if len(line) != expectedLen {
			t.Errorf("row %d: line length %d, want %d (line=%q)", i, len(line), expectedLen, line)
		}
	}

	// Bracket positions must be identical on every row so the agent column
	// stays vertically aligned.
	for i := range lines {
		if lines[i][agentBracketOff] != '[' {
			t.Errorf("row %d: expected '[' at offset %d, got %q (line=%q)", i, agentBracketOff, lines[i][agentBracketOff], lines[i])
		}
		if lines[i][agentCloseOff] != ']' {
			t.Errorf("row %d: expected ']' at offset %d, got %q (line=%q)", i, agentCloseOff, lines[i][agentCloseOff], lines[i])
		}
	}

	// Cost column on the $1.45 row must be fully rendered (not "$1...").
	costLine := lines[2]
	costField := costLine[costStart : costStart+costWidth]
	if !strings.Contains(costField, "$1.45") {
		t.Errorf("expected $1.45 in cost field, got %q (full=%q)", costField, costLine)
	}
	if strings.Contains(costField, "...") {
		t.Errorf("cost field should not be truncated, got %q", costField)
	}

	// Status column on the "reviewing" row must contain "reviewing" in full.
	statusField := costLine[statusStart : statusStart+statusWidth]
	if !strings.HasPrefix(statusField, "reviewing") {
		t.Errorf("expected status 'reviewing' at offset %d, got %q (line=%q)", statusStart, statusField, costLine)
	}

	// PR field for the "reviewing" row must start at the same offset on
	// every row that has a PR.
	prField := costLine[prStart : prStart+prWidth]
	if !strings.HasPrefix(prField, "PR#1240") {
		t.Errorf("expected PR#1240 at offset %d, got %q (line=%q)", prStart, prField, costLine)
	}
	// Cheap row with no PR must have a blank PR field at the same offset.
	noPRRow := lines[0]
	noPRField := noPRRow[prStart : prStart+prWidth]
	if strings.TrimSpace(noPRField) != "" {
		t.Errorf("expected empty PR field for no-PR row, got %q (line=%q)", noPRField, noPRRow)
	}

	// Overflow agent must be truncated to fit agentWidth.
	overflowRow := lines[4]
	agentField := overflowRow[agentStart : agentStart+agentWidth]
	if len(agentField) != agentWidth {
		t.Errorf("agent field width %d, want %d", len(agentField), agentWidth)
	}
	if !strings.HasSuffix(strings.TrimRight(agentField, " "), "...") {
		t.Errorf("expected overflow agent to be truncated with '...', got %q", agentField)
	}

	// dependency-updater (18 chars) exceeds agentWidth and must be
	// ellipsized, not allowed to push the column wider.
	builtinRow := lines[3]
	builtinAgent := builtinRow[agentStart : agentStart+agentWidth]
	if !strings.HasSuffix(strings.TrimRight(builtinAgent, " "), "...") {
		t.Errorf("expected dependency-updater to be truncated with '...', got %q", builtinAgent)
	}

	// 9-char agent names (autopilot, bug-fixer) must fit without truncation.
	autopilotRow := lines[2]
	autopilotAgent := autopilotRow[agentStart : agentStart+agentWidth]
	if strings.Contains(autopilotAgent, "...") {
		t.Errorf("autopilot should fit without truncation at width %d, got %q", agentWidth, autopilotAgent)
	}
	if strings.TrimSpace(autopilotAgent) != "autopilot" {
		t.Errorf("expected agent 'autopilot', got %q", autopilotAgent)
	}

	// Big cost ($12.34) row must render fully right-aligned.
	bigCostRow := lines[5]
	bigCostField := bigCostRow[costStart : costStart+costWidth]
	if !strings.Contains(bigCostField, "$12.34") {
		t.Errorf("expected $12.34 in cost field, got %q", bigCostField)
	}
	// Right-aligned: cost should end with the last char of the number.
	if bigCostField[costWidth-1] != '4' {
		t.Errorf("cost field should be right-aligned (last char '4'), got %q", bigCostField)
	}
}

func TestRenderHeader(t *testing.T) {
	titleWidth := 35
	header := renderHeader(titleWidth)
	row := renderRow(jobColumns{
		label: "#123", agent: "autopilot", title: "demo",
		status: "queued", pr: "PR#1", cost: "$1.00",
	}, titleWidth)

	if len(header) != len(row) {
		t.Fatalf("header length %d != row length %d\nheader=%q\nrow   =%q", len(header), len(row), header, row)
	}

	// Column labels must sit at the same byte offsets as the row's column
	// content so the header lines up with what's below it.
	cases := []struct {
		label string
		off   int
	}{
		{"ISSUE", 0},
		{"AGENT", labelWidth + 2 + 1}, // 9
		{"TITLE", labelWidth + 2 + 1 + agentWidth + 1 + 2},                                 // 21
		{"STATUS", labelWidth + 2 + 1 + agentWidth + 1 + 2 + titleWidth + 2},               // 58
		{"PR", labelWidth + 2 + 1 + agentWidth + 1 + 2 + titleWidth + 2 + statusWidth + 2}, // 70
	}
	for _, c := range cases {
		got := header[c.off : c.off+len(c.label)]
		if got != c.label {
			t.Errorf("header[%d:%d] = %q, want %q (full header=%q)", c.off, c.off+len(c.label), got, c.label, header)
		}
	}

	// COST is right-aligned in the cost column.
	costEnd := len(header)
	costField := header[costEnd-costWidth : costEnd]
	if strings.TrimLeft(costField, " ") != "COST" {
		t.Errorf("expected right-aligned 'COST' in last %d chars, got %q", costWidth, costField)
	}
}

func TestFormatRemoteJobLineColumns(t *testing.T) {
	titleWidth := 30
	jobs := []daemon.JobResponse{
		{Agent: "spike", Name: "spike-issue-518", IssueNumber: 518, Title: "Investigate", Status: "done", CostUSD: 0.88},
		{Agent: "autopilot", Name: "autopilot-issue-1234", IssueNumber: 1234, Title: "Polish picker", Status: "reviewing", PRNumber: 1240, CostUSD: 1.45},
	}

	expectedLen := fixedColumnWidth + titleWidth
	for i := range jobs {
		line := formatRemoteJobLine(&jobs[i], titleWidth)
		if len(line) != expectedLen {
			t.Errorf("remote row %d: line length %d, want %d (line=%q)", i, len(line), expectedLen, line)
		}
		if line[8] != '[' {
			t.Errorf("remote row %d: expected '[' at offset 8, got %q", i, line[8])
		}
	}

	// $1.45 must show in full in the right-aligned cost field.
	line := formatRemoteJobLine(&jobs[1], titleWidth)
	costStart := fixedColumnWidth + titleWidth - costWidth
	if !strings.Contains(line[costStart:], "$1.45") {
		t.Errorf("expected $1.45 at end of remote line, got %q", line)
	}
}
