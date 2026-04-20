package poller

// Schedule firing logic adapted from Launch's schedule service.
// Pure functions with no external dependencies — determines whether
// a schedule should fire based on its configuration, last fire time,
// and current time.

import (
	"strconv"
	"strings"
	"time"
)

// ScheduleConfig holds the timing configuration for a schedule.
type ScheduleConfig struct {
	Mode       string // "interval", "daily", "weekly"
	Interval   string // e.g. "15"
	Unit       string // "minutes", "hours", "days"
	TimeOfDay  string // "HH:MM" 24-hour format
	DaysOfWeek string // "monday,wednesday"
}

// ShouldFire determines whether a schedule should fire based on its
// configuration, the last time it fired, and the current time.
func ShouldFire(cfg ScheduleConfig, lastFired time.Time, now time.Time) bool {
	switch cfg.Mode {
	case "interval":
		return shouldFireInterval(cfg, lastFired, now)
	case "daily":
		return shouldFireDaily(cfg, lastFired, now)
	case "weekly":
		return shouldFireWeekly(cfg, lastFired, now)
	default:
		return false
	}
}

func shouldFireInterval(cfg ScheduleConfig, lastFired time.Time, now time.Time) bool {
	interval, err := strconv.Atoi(cfg.Interval)
	if err != nil || interval <= 0 {
		return false
	}

	var duration time.Duration
	switch cfg.Unit {
	case "minutes":
		duration = time.Duration(interval) * time.Minute
	case "hours":
		duration = time.Duration(interval) * time.Hour
	case "days":
		duration = time.Duration(interval) * 24 * time.Hour
	default:
		return false
	}

	return now.Sub(lastFired) >= duration
}

func shouldFireDaily(cfg ScheduleConfig, lastFired time.Time, now time.Time) bool {
	if cfg.TimeOfDay == "" {
		return false
	}

	targetHour, targetMin := parseTimeOfDay(cfg.TimeOfDay)
	if targetHour < 0 {
		return false
	}

	target := time.Date(now.Year(), now.Month(), now.Day(), targetHour, targetMin, 0, 0, now.Location())
	return now.After(target) && lastFired.Before(target)
}

func shouldFireWeekly(cfg ScheduleConfig, lastFired time.Time, now time.Time) bool {
	if cfg.TimeOfDay == "" || cfg.DaysOfWeek == "" {
		return false
	}

	days := strings.Split(cfg.DaysOfWeek, ",")
	todayName := strings.ToLower(now.Weekday().String())
	dayMatch := false
	for _, d := range days {
		if strings.ToLower(strings.TrimSpace(d)) == todayName {
			dayMatch = true
			break
		}
	}

	if !dayMatch {
		return false
	}

	return shouldFireDaily(cfg, lastFired, now)
}

func parseTimeOfDay(tod string) (int, int) {
	parts := strings.SplitN(tod, ":", 2)
	if len(parts) != 2 {
		return -1, -1
	}

	hour := 0
	min := 0
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return -1, -1
		}
		hour = hour*10 + int(c-'0')
	}
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return -1, -1
		}
		min = min*10 + int(c-'0')
	}

	if hour > 23 || min > 59 {
		return -1, -1
	}

	return hour, min
}
