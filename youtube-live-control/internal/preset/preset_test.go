package preset

import (
	"errors"
	"testing"
	"time"
)

func sample() Preset {
	return Preset{Name: "Sunday", TitleTemplate: "Sunday Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30"}
}

func TestStoreRoundTrip(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(sample())
	if err != nil || saved.ID == "" {
		t.Fatalf("Save: %v (%+v)", err, saved)
	}
	if _, err := store.SetThumbnail(saved.ID, ".png", []byte("png")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(saved.ID)
	if err != nil || got.Name != "Sunday" || got.ThumbnailFile != saved.ID+".png" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	image, ct, ok, err := store.Thumbnail(saved.ID)
	if err != nil || !ok || ct != "image/png" || string(image) != "png" {
		t.Fatalf("Thumbnail = %q %q %v %v", image, ct, ok, err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if err := store.Delete(saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestValidate(t *testing.T) {
	bad := []Preset{
		{},
		{Name: "x", TitleTemplate: "t", Privacy: "secret", StreamID: "s", TimeOfDay: "09:30"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "", TimeOfDay: "09:30"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "s", TimeOfDay: "9.30"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "s", TimeOfDay: "09:15"},
		{Name: "x", TitleTemplate: "t", Privacy: "public", StreamID: "s", TimeOfDay: "02:00"},
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

func TestGetRejectsPathLikeIDs(t *testing.T) {
	store, _ := Open(t.TempDir())
	for _, id := range []string{"../x", "a/b", "", "ZZ"} {
		if _, err := store.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
}
