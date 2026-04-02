// Package scheduler provides cron expression parsing and job scheduling.
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpr represents a parsed 5-field cron expression.
// Fields: minute (0-59), hour (0-23), day-of-month (1-31), month (1-12), day-of-week (0-6, 0=Sunday).
type CronExpr struct {
	Minute     fieldMatcher
	Hour       fieldMatcher
	DayOfMonth fieldMatcher
	Month      fieldMatcher
	DayOfWeek  fieldMatcher
	raw        string
}

// ParseCron parses a standard 5-field cron expression.
// Supports: * (any), ranges (1-5), lists (1,3,5), steps (*/5, 1-10/2).
func ParseCron(expr string) (*CronExpr, error) {
	expr = strings.TrimSpace(expr)
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron: expected 5 fields, got %d in %q", len(fields), expr)
	}

	minute, err := parseField(fields[0], 0, 59)
	if err != nil {
		return nil, fmt.Errorf("cron minute: %w", err)
	}
	hour, err := parseField(fields[1], 0, 23)
	if err != nil {
		return nil, fmt.Errorf("cron hour: %w", err)
	}
	dom, err := parseField(fields[2], 1, 31)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-month: %w", err)
	}
	month, err := parseField(fields[3], 1, 12)
	if err != nil {
		return nil, fmt.Errorf("cron month: %w", err)
	}
	dow, err := parseField(fields[4], 0, 6)
	if err != nil {
		return nil, fmt.Errorf("cron day-of-week: %w", err)
	}

	return &CronExpr{
		Minute:     minute,
		Hour:       hour,
		DayOfMonth: dom,
		Month:      month,
		DayOfWeek:  dow,
		raw:        expr,
	}, nil
}

// Match returns true if the given time matches the cron expression.
// Time is evaluated in UTC.
func (c *CronExpr) Match(t time.Time) bool {
	t = t.UTC()
	return c.Minute.match(t.Minute()) &&
		c.Hour.match(t.Hour()) &&
		c.DayOfMonth.match(t.Day()) &&
		c.Month.match(int(t.Month())) &&
		c.DayOfWeek.match(int(t.Weekday()))
}

// NextAfter returns the next time after t that matches the cron expression.
// Searches up to 366 days ahead. Returns zero time if not found.
func (c *CronExpr) NextAfter(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute) // start from next minute

	// Search up to 366 days * 24 hours * 60 minutes = 527040 minutes.
	for i := 0; i < 527040; i++ {
		if c.Match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// String returns the original cron expression.
func (c *CronExpr) String() string {
	return c.raw
}

// --- Field parsing ---

// fieldMatcher matches values for a single cron field.
type fieldMatcher struct {
	values map[int]bool // nil means "any" (wildcard)
}

func (f fieldMatcher) match(val int) bool {
	if f.values == nil {
		return true // wildcard
	}
	return f.values[val]
}

// parseField parses a single cron field with support for *, ranges, lists, steps.
func parseField(field string, min, max int) (fieldMatcher, error) {
	if field == "*" {
		return fieldMatcher{}, nil // wildcard
	}

	values := make(map[int]bool)

	// Split by comma for lists: "1,3,5"
	parts := strings.Split(field, ",")
	for _, part := range parts {
		if err := parsePart(part, min, max, values); err != nil {
			return fieldMatcher{}, err
		}
	}

	if len(values) == 0 {
		return fieldMatcher{}, fmt.Errorf("empty field %q", field)
	}
	return fieldMatcher{values: values}, nil
}

// parsePart parses a single element: number, range (1-5), or step (*/5, 1-10/2).
func parsePart(part string, min, max int, values map[int]bool) error {
	// Check for step: "*/5" or "1-10/2"
	if idx := strings.Index(part, "/"); idx >= 0 {
		base := part[:idx]
		stepStr := part[idx+1:]
		step, err := strconv.Atoi(stepStr)
		if err != nil || step < 1 {
			return fmt.Errorf("invalid step %q", stepStr)
		}

		rangeMin, rangeMax := min, max
		if base != "*" {
			rangeMin, rangeMax, err = parseRange(base, min, max)
			if err != nil {
				return err
			}
		}

		for v := rangeMin; v <= rangeMax; v += step {
			values[v] = true
		}
		return nil
	}

	// Check for range: "1-5"
	if strings.Contains(part, "-") {
		lo, hi, err := parseRange(part, min, max)
		if err != nil {
			return err
		}
		for v := lo; v <= hi; v++ {
			values[v] = true
		}
		return nil
	}

	// Single value.
	v, err := strconv.Atoi(part)
	if err != nil {
		return fmt.Errorf("invalid value %q", part)
	}
	if v < min || v > max {
		return fmt.Errorf("value %d out of range [%d, %d]", v, min, max)
	}
	values[v] = true
	return nil
}

func parseRange(s string, min, max int) (int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range %q", s)
	}
	lo, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range start %q", parts[0])
	}
	hi, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid range end %q", parts[1])
	}
	if lo < min || hi > max || lo > hi {
		return 0, 0, fmt.Errorf("range %d-%d out of bounds [%d, %d]", lo, hi, min, max)
	}
	return lo, hi, nil
}
