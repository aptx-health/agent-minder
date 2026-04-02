package scheduler

import (
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every minute", "* * * * *", false},
		{"specific time", "30 9 * * *", false},
		{"monday 9am", "0 9 * * 1", false},
		{"every 5 min", "*/5 * * * *", false},
		{"range", "0 9-17 * * *", false},
		{"list", "0 9,12,17 * * *", false},
		{"complex", "*/15 9-17 * * 1-5", false},
		{"first of month", "0 0 1 * *", false},

		// Errors.
		{"too few fields", "* * *", true},
		{"too many fields", "* * * * * *", true},
		{"empty", "", true},
		{"bad minute", "60 * * * *", true},
		{"bad hour", "* 25 * * *", true},
		{"bad dom", "* * 32 * *", true},
		{"bad month", "* * * 13 *", true},
		{"bad dow", "* * * * 7", true},
		{"bad step", "*/0 * * * *", true},
		{"bad range", "* 5-3 * * *", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseCron(tt.expr)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCronMatch(t *testing.T) {
	// Monday 2026-04-06 at 09:30 UTC.
	mon := time.Date(2026, 4, 6, 9, 30, 0, 0, time.UTC)
	// Friday 2026-04-10 at 12:00 UTC.
	fri := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	// Sunday 2026-04-05 at 06:00 UTC.
	sun := time.Date(2026, 4, 5, 6, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		expr  string
		t     time.Time
		match bool
	}{
		{"every minute matches anything", "* * * * *", mon, true},
		{"specific match", "30 9 * * *", mon, true},
		{"specific no match minute", "0 9 * * *", mon, false},
		{"specific no match hour", "30 10 * * *", mon, false},
		{"monday match", "30 9 * * 1", mon, true},
		{"monday no match friday", "30 9 * * 1", fri, false},
		{"friday match", "0 12 * * 5", fri, true},
		{"sunday match dow=0", "0 6 * * 0", sun, true},
		{"every 5 min match", "*/5 * * * *", mon, true},                      // 30 is divisible by 5
		{"every 5 min no match", "*/5 * * * *", mon.Add(time.Minute), false}, // 31 is not
		{"range match", "* 9-17 * * *", mon, true},
		{"range no match", "* 9-17 * * *", sun, false}, // 6am
		{"list match", "30 9,12,17 * * *", mon, true},
		{"list no match", "30 10,11,13 * * *", mon, false},
		{"day of month", "* * 6 * *", mon, true}, // April 6
		{"day of month no", "* * 7 * *", mon, false},
		{"month match", "* * * 4 *", mon, true}, // April
		{"month no match", "* * * 3 *", mon, false},
		{"complex weekday range", "*/15 9-17 * * 1-5", mon, true},
		{"complex weekend fail", "*/15 9-17 * * 1-5", sun, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := c.Match(tt.t); got != tt.match {
				t.Errorf("Match(%v) = %v, want %v", tt.t, got, tt.match)
			}
		})
	}
}

func TestCronNextAfter(t *testing.T) {
	// Start: Monday 2026-04-06 at 09:28 UTC.
	start := time.Date(2026, 4, 6, 9, 28, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr string
		want time.Time
	}{
		{
			"next 30 past",
			"30 9 * * *",
			time.Date(2026, 4, 6, 9, 30, 0, 0, time.UTC),
		},
		{
			"next hour",
			"0 10 * * *",
			time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC),
		},
		{
			"next friday noon",
			"0 12 * * 5",
			time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC),
		},
		{
			"every 15 min",
			"*/15 * * * *",
			time.Date(2026, 4, 6, 9, 30, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseCron(tt.expr)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := c.NextAfter(start)
			if !got.Equal(tt.want) {
				t.Errorf("NextAfter = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCronString(t *testing.T) {
	c, _ := ParseCron("0 9 * * 1")
	if c.String() != "0 9 * * 1" {
		t.Errorf("String() = %q, want %q", c.String(), "0 9 * * 1")
	}
}
