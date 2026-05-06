package poller

import (
	"testing"
	"time"
)

func TestShouldFire_Interval(t *testing.T) {
	cfg := ScheduleConfig{Mode: "interval", Interval: "30", Unit: "minutes"}
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)

	// 30 minutes ago — should fire.
	lastFired := now.Add(-30 * time.Minute)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire after 30 minutes elapsed")
	}

	// 29 minutes ago — should NOT fire.
	lastFired = now.Add(-29 * time.Minute)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire after only 29 minutes")
	}

	// 1 hour ago — should fire (overdue).
	lastFired = now.Add(-60 * time.Minute)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire when overdue")
	}
}

func TestShouldFire_IntervalHours(t *testing.T) {
	cfg := ScheduleConfig{Mode: "interval", Interval: "2", Unit: "hours"}
	now := time.Date(2026, 4, 19, 14, 0, 0, 0, time.UTC)

	lastFired := now.Add(-2 * time.Hour)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire after 2 hours")
	}

	lastFired = now.Add(-1 * time.Hour)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire after only 1 hour")
	}
}

func TestShouldFire_IntervalDays(t *testing.T) {
	cfg := ScheduleConfig{Mode: "interval", Interval: "1", Unit: "days"}
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)

	lastFired := now.Add(-25 * time.Hour)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire after 25 hours")
	}

	lastFired = now.Add(-23 * time.Hour)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire after only 23 hours")
	}
}

func TestShouldFire_IntervalInvalid(t *testing.T) {
	// Invalid interval value.
	cfg := ScheduleConfig{Mode: "interval", Interval: "abc", Unit: "minutes"}
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	lastFired := now.Add(-1 * time.Hour)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire with invalid interval")
	}

	// Invalid unit.
	cfg = ScheduleConfig{Mode: "interval", Interval: "10", Unit: "weeks"}
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire with invalid unit")
	}

	// Zero interval.
	cfg = ScheduleConfig{Mode: "interval", Interval: "0", Unit: "minutes"}
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire with zero interval")
	}
}

func TestShouldFire_Daily(t *testing.T) {
	cfg := ScheduleConfig{Mode: "daily", TimeOfDay: "08:00"}
	loc := time.UTC

	// It's 08:30, last fired at 07:30 (before today's 08:00) — should fire.
	now := time.Date(2026, 4, 19, 8, 30, 0, 0, loc)
	lastFired := time.Date(2026, 4, 19, 7, 30, 0, 0, loc)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire: now is past 08:00 and lastFired is before 08:00")
	}

	// It's 08:30, last fired at 08:15 (after today's 08:00) — should NOT fire.
	lastFired = time.Date(2026, 4, 19, 8, 15, 0, 0, loc)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire: already fired after today's target")
	}

	// It's 07:30 (before target) — should NOT fire.
	now = time.Date(2026, 4, 19, 7, 30, 0, 0, loc)
	lastFired = time.Date(2026, 4, 18, 8, 30, 0, 0, loc)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire: target time hasn't passed yet today")
	}
}

func TestShouldFire_DailyMissedDay(t *testing.T) {
	cfg := ScheduleConfig{Mode: "daily", TimeOfDay: "09:00"}

	// API was down yesterday. Last fired 2 days ago. Now is 09:30 today — should fire.
	now := time.Date(2026, 4, 19, 9, 30, 0, 0, time.UTC)
	lastFired := time.Date(2026, 4, 17, 9, 15, 0, 0, time.UTC)
	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire after missing a day")
	}
}

func TestShouldFire_Weekly(t *testing.T) {
	// 19 April 2026 is a Sunday.
	cfg := ScheduleConfig{Mode: "weekly", TimeOfDay: "09:00", DaysOfWeek: "sunday"}
	now := time.Date(2026, 4, 19, 9, 30, 0, 0, time.UTC)
	lastFired := time.Date(2026, 4, 12, 9, 15, 0, 0, time.UTC)

	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire: it's Sunday after 09:00 and lastFired is before today's 09:00")
	}

	// Same day but before target — should NOT fire.
	now = time.Date(2026, 4, 19, 8, 30, 0, 0, time.UTC)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire: target time hasn't passed yet")
	}

	// Wrong day (Saturday) — should NOT fire.
	cfg.DaysOfWeek = "monday,wednesday"
	now = time.Date(2026, 4, 19, 9, 30, 0, 0, time.UTC)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire: Sunday is not in the configured days")
	}
}

func TestShouldFire_WeeklyMultipleDays(t *testing.T) {
	// 20 April 2026 is a Monday.
	cfg := ScheduleConfig{Mode: "weekly", TimeOfDay: "10:00", DaysOfWeek: "monday,wednesday,friday"}
	now := time.Date(2026, 4, 20, 10, 30, 0, 0, time.UTC)
	lastFired := time.Date(2026, 4, 17, 10, 15, 0, 0, time.UTC) // Last Friday

	if !ShouldFire(cfg, lastFired, now) {
		t.Error("expected fire: Monday is in the list and target time passed")
	}
}

func TestShouldFire_InvalidMode(t *testing.T) {
	cfg := ScheduleConfig{Mode: "monthly"}
	now := time.Date(2026, 4, 19, 10, 0, 0, 0, time.UTC)
	lastFired := now.Add(-30 * 24 * time.Hour)
	if ShouldFire(cfg, lastFired, now) {
		t.Error("expected no fire for unsupported mode")
	}
}

func TestParseTimeOfDay(t *testing.T) {
	tests := []struct {
		input string
		hour  int
		min   int
	}{
		{"08:00", 8, 0},
		{"23:59", 23, 59},
		{"00:00", 0, 0},
		{"12:30", 12, 30},
		{"24:00", -1, -1}, // invalid hour
		{"12:60", -1, -1}, // invalid minute
		{"abc", -1, -1},   // no colon
		{"12:ab", -1, -1}, // non-numeric
		{"ab:00", -1, -1}, // non-numeric
	}

	for _, tt := range tests {
		h, m := parseTimeOfDay(tt.input)
		if h != tt.hour || m != tt.min {
			t.Errorf("parseTimeOfDay(%q) = (%d, %d), want (%d, %d)", tt.input, h, m, tt.hour, tt.min)
		}
	}
}
