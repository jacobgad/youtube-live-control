package preset

import (
	"testing"
	"time"
)

func sample() Preset {
	return Preset{Name: "Sunday", TitleTemplate: "Sunday Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30"}
}

func TestValidate(t *testing.T) {
	bad := []Preset{
		{},
		{Name: "x", TitleTemplate: "t", Privacy: "secret", StreamID: "s", TimeOfDay: "09:30"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "", TimeOfDay: "09:30"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "s", TimeOfDay: "9.30"},
	}
	quarter := sample()
	quarter.TimeOfDay = "09:15"
	if err := quarter.Validate(); err != nil {
		t.Fatalf("any minute is a valid usual time now: %v", err)
	}
	for i, p := range bad {
		if p.Validate() == nil {
			t.Fatalf("case %d accepted: %+v", i, p)
		}
	}
	if err := sample().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTitleAndNextStart(t *testing.T) {
	p := sample()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.Local) // a Wednesday
	next := p.NextStart(now)
	if next.Weekday() != time.Sunday || next.Hour() != 9 || next.Minute() != 30 || next.Day() != 5 {
		t.Fatalf("NextStart = %v", next)
	}
	if got := p.Title(next); got != "Sunday Service – 5 Jan 2025" {
		t.Fatalf("Title = %q", got)
	}
	sameDayLater := time.Date(2025, 1, 5, 8, 0, 0, 0, time.Local)
	if got := p.NextStart(sameDayLater); got.Day() != 5 {
		t.Fatalf("same-day earlier than start should pick today, got %v", got)
	}
	sameDayPast := time.Date(2025, 1, 5, 10, 0, 0, 0, time.Local)
	if got := p.NextStart(sameDayPast); got.Day() != 12 {
		t.Fatalf("same-day after start should pick next week, got %v", got)
	}
}
