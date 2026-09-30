package store

import (
	"context"
	"database/sql"
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
	img, err := s.AddImage(ctx, "image/png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	p := sample()
	p.ImageID = img.ID
	saved, err := s.SavePreset(ctx, p)
	if err != nil || saved.ID == "" {
		t.Fatalf("SavePreset: %v (%+v)", err, saved)
	}
	saved.Name = "Sunday morning"
	if _, err := s.SavePreset(ctx, saved); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPreset(ctx, saved.ID)
	if err != nil || got.Name != "Sunday morning" || got.ImageID != img.ID || got.Weekday != time.Sunday {
		t.Fatalf("GetPreset = %+v, %v", got, err)
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
	if _, _, err := s.ImageBytes(ctx, img.ID); err != nil {
		t.Fatal("deleting a preset must not delete its library image")
	}
}

func TestImageLibrary(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	img, err := s.AddImage(ctx, "image/jpeg", []byte("jpg"))
	if err != nil || img.File != img.ID+".jpg" || img.Size != 3 || img.UsedBy != 0 {
		t.Fatalf("AddImage = %+v %v", img, err)
	}
	data, ct, err := s.ImageBytes(ctx, img.ID)
	if err != nil || ct != "image/jpeg" || string(data) != "jpg" {
		t.Fatalf("ImageBytes = %q %q %v", data, ct, err)
	}
	p := sample()
	p.ImageID = img.ID
	saved, _ := s.SavePreset(ctx, p)
	if got, _ := s.GetImage(ctx, img.ID); got.UsedBy != 1 {
		t.Fatalf("UsedBy = %d", got.UsedBy)
	}
	if err := s.DeleteImage(ctx, img.ID); !errors.Is(err, ErrImageInUse) {
		t.Fatalf("deleting an in-use image: %v", err)
	}
	copied, _ := s.DuplicatePreset(ctx, saved.ID)
	if copied.ImageID != img.ID {
		t.Fatal("duplicate should share the image")
	}
	if got, _ := s.GetImage(ctx, img.ID); got.UsedBy != 2 {
		t.Fatalf("UsedBy after duplicate = %d", got.UsedBy)
	}
	_ = s.DeletePreset(ctx, saved.ID)
	_ = s.DeletePreset(ctx, copied.ID)
	if err := s.DeleteImage(ctx, img.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(s.images); len(entries) != 0 {
		t.Fatalf("image file not removed: %v", entries)
	}
	if _, err := s.GetImage(ctx, img.ID); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestSavePresetRejectsUnknownImage(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	p := sample()
	p.ImageID = "abcdef012345"
	if _, err := s.SavePreset(ctx, p); !errors.Is(err, ErrImageNotFound) {
		t.Fatalf("unknown image accepted: %v", err)
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

func TestMigrationFromSchemaOneCarriesThumbnailsIntoTheLibrary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "ylc.sqlite")
	images := filepath.Join(dir, "images")
	if err := os.MkdirAll(images, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO presets (id, name, title_template, description, privacy, stream_id, weekday, time_of_day, thumbnail_file, created_at, updated_at) VALUES ('aaaaaaaaaaaa', 'Sunday', 't', '', 'public', 's1', 0, '09:30', 'aaaaaaaaaaaa-bbbbbbbbbbbb.png', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := os.WriteFile(filepath.Join(images, "aaaaaaaaaaaa-bbbbbbbbbbbb.png"), []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path, images)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.GetPreset(ctx, "aaaaaaaaaaaa")
	if err != nil || p.ImageID == "" {
		t.Fatalf("migrated preset = %+v %v", p, err)
	}
	data, ct, err := s.ImageBytes(ctx, p.ImageID)
	if err != nil || ct != "image/png" || string(data) != "png" {
		t.Fatalf("migrated image = %q %q %v", data, ct, err)
	}
	list, _ := s.ListImages(ctx)
	if len(list) != 1 || list[0].UsedBy != 1 {
		t.Fatalf("library after migration = %+v", list)
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
			t.Fatalf("preset id %q: %v", id, err)
		}
		if _, err := s.GetImage(ctx, id); !errors.Is(err, ErrImageNotFound) {
			t.Fatalf("image id %q: %v", id, err)
		}
	}
}
