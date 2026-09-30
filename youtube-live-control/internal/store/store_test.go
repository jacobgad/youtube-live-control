package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/preset"
)

func open(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "ylc.sqlite"), filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sample() preset.Preset {
	return preset.Preset{Name: "Sunday", TitleTemplate: "Sunday Service – {date}", Privacy: "public", StreamID: "s1", Weekday: time.Sunday, TimeOfDay: "09:30"}
}

func TestPresetRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	saved, err := s.SavePreset(ctx, sample())
	if err != nil || saved.ID == "" {
		t.Fatalf("SavePreset: %v (%+v)", err, saved)
	}
	saved.Name = "Sunday morning"
	if _, err := s.SavePreset(ctx, saved); err != nil {
		t.Fatal(err)
	}
	withThumb, err := s.SetThumbnail(ctx, saved.ID, ".png", []byte("png"))
	if err != nil || withThumb.ThumbnailFile == "" {
		t.Fatalf("SetThumbnail: %v %+v", err, withThumb)
	}
	got, err := s.GetPreset(ctx, saved.ID)
	if err != nil || got.Name != "Sunday morning" || got.ThumbnailFile != withThumb.ThumbnailFile || got.Weekday != time.Sunday {
		t.Fatalf("GetPreset = %+v, %v", got, err)
	}
	image, ct, ok, err := s.Thumbnail(ctx, saved.ID)
	if err != nil || !ok || ct != "image/png" || string(image) != "png" {
		t.Fatalf("Thumbnail = %q %q %v %v", image, ct, ok, err)
	}
	list, err := s.ListPresets(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListPresets = %v, %v", list, err)
	}
	if err := s.DeletePreset(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPreset(ctx, saved.ID); !errors.Is(err, preset.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if entries, _ := os.ReadDir(s.images); len(entries) != 0 {
		t.Fatalf("image not removed: %v", entries)
	}
}

func TestReplacingThumbnailRemovesOldFile(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p, _ := s.SavePreset(ctx, sample())
	first, _ := s.SetThumbnail(ctx, p.ID, ".jpg", []byte("a"))
	second, _ := s.SetThumbnail(ctx, p.ID, ".png", []byte("b"))
	if first.ThumbnailFile == second.ThumbnailFile {
		t.Fatal("thumbnail file name must change so browsers do not cache the old image")
	}
	entries, _ := os.ReadDir(s.images)
	if len(entries) != 1 || entries[0].Name() != second.ThumbnailFile {
		t.Fatalf("images dir = %v", entries)
	}
}

func TestDuplicateCopiesRecordAndImage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	original, _ := s.SavePreset(ctx, sample())
	if _, err := s.SetThumbnail(ctx, original.ID, ".png", []byte("png")); err != nil {
		t.Fatal(err)
	}
	copied, err := s.DuplicatePreset(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == original.ID || copied.Name != "Sunday (copy)" || copied.StreamID != original.StreamID {
		t.Fatalf("copy = %+v", copied)
	}
	if err := s.DeletePreset(ctx, original.ID); err != nil {
		t.Fatal(err)
	}
	if image, _, ok, _ := s.Thumbnail(ctx, copied.ID); !ok || string(image) != "png" {
		t.Fatal("deleting the original must not remove the copy's image")
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, ok, err := s.Setting(ctx, KeyLastPreset); ok || err != nil {
		t.Fatalf("unset setting: %v %v", ok, err)
	}
	if err := s.SetSetting(ctx, KeyLastPreset, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, KeyLastPreset, "def"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Setting(ctx, KeyLastPreset); !ok || v != "def" {
		t.Fatalf("setting = %q %v", v, ok)
	}
	if err := s.DeleteSetting(ctx, KeyLastPreset); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Setting(ctx, KeyLastPreset); ok {
		t.Fatal("setting not deleted")
	}
}

func TestReopenKeepsDataAndSchemaVersion(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "ylc.sqlite")
	s, err := Open(ctx, path, filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePreset(ctx, sample()); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path, filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, _ := s.ListPresets(ctx)
	if len(list) != 1 {
		t.Fatalf("presets lost across reopen: %v", list)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("user_version = %d %v", version, err)
	}
}

func TestInvalidIDsAreNotFound(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, id := range []string{"", "../x", "ZZ", "abc/def"} {
		if _, err := s.GetPreset(ctx, id); !errors.Is(err, preset.ErrNotFound) {
			t.Fatalf("id %q: %v", id, err)
		}
	}
}
