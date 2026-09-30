// Package store persists everything the add-on remembers — presets, the refresh
// token and small settings — in one SQLite file under /data, with thumbnail images
// beside it in one directory. Two paths to back up; one schema version to migrate.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/atomicfile"
	"github.com/jacobgad/youtube-live-control/internal/preset"
	_ "modernc.org/sqlite"
)

// Settings keys.
const (
	KeyRefreshToken = "refresh_token"
	KeyLastPreset   = "last_preset_id"
)

const schemaVersion = 1

var migrations = []string{
	`CREATE TABLE presets (
		id TEXT PRIMARY KEY NOT NULL,
		name TEXT NOT NULL,
		title_template TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		privacy TEXT NOT NULL,
		stream_id TEXT NOT NULL,
		weekday INTEGER NOT NULL,
		time_of_day TEXT NOT NULL,
		thumbnail_file TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE settings (
		key TEXT PRIMARY KEY NOT NULL,
		value TEXT NOT NULL
	)`,
}

// Store is the open database plus the images directory.
type Store struct {
	db     *sql.DB
	images string
	now    func() time.Time
}

// Open opens or creates the database, brings the schema up to date and ensures the
// images directory exists.
func Open(ctx context.Context, path, imagesDir string) (*Store, error) {
	if err := os.MkdirAll(imagesDir, 0o700); err != nil {
		return nil, fmt.Errorf("images dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// why: one pooled connection avoids SQLITE_BUSY between the web and controller goroutines.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000", "PRAGMA foreign_keys = ON"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return nil, errors.Join(fmt.Errorf("%s: %w", pragma, err), db.Close())
		}
	}
	if err := migrate(ctx, db); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{db: db, images: imagesDir, now: time.Now}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", current, schemaVersion)
	}
	for v := current; v < schemaVersion; v++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[v]); err != nil {
			return errors.Join(fmt.Errorf("migration %d: %w", v+1, err), tx.Rollback())
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return errors.Join(err, tx.Rollback())
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Setting returns a settings value; ok is false when unset.
func (s *Store) Setting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetSetting upserts a settings value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// LoadToken makes the Store the Auth's TokenStore.
func (s *Store) LoadToken(ctx context.Context) (string, bool, error) {
	return s.Setting(ctx, KeyRefreshToken)
}

// SaveToken persists the refresh token.
func (s *Store) SaveToken(ctx context.Context, token string) error {
	return s.SetSetting(ctx, KeyRefreshToken, token)
}

// ClearToken forgets the refresh token.
func (s *Store) ClearToken(ctx context.Context) error {
	return s.DeleteSetting(ctx, KeyRefreshToken)
}

// DeleteSetting removes a settings value; a missing key is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return err
}

const presetColumns = "id, name, title_template, description, privacy, stream_id, weekday, time_of_day, thumbnail_file"

func scanPreset(row interface{ Scan(...any) error }) (preset.Preset, error) {
	var p preset.Preset
	var weekday int
	err := row.Scan(&p.ID, &p.Name, &p.TitleTemplate, &p.Description, &p.Privacy, &p.StreamID, &weekday, &p.TimeOfDay, &p.ThumbnailFile)
	p.Weekday = time.Weekday(weekday)
	return p, err
}

// ListPresets returns every preset sorted by name (case-insensitive).
func (s *Store) ListPresets(ctx context.Context) ([]preset.Preset, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+presetColumns+" FROM presets ORDER BY lower(name), id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []preset.Preset
	for rows.Next() {
		p, err := scanPreset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPreset loads one preset.
func (s *Store) GetPreset(ctx context.Context, id string) (preset.Preset, error) {
	if !validID(id) {
		return preset.Preset{}, preset.ErrNotFound
	}
	p, err := scanPreset(s.db.QueryRowContext(ctx, "SELECT "+presetColumns+" FROM presets WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return preset.Preset{}, preset.ErrNotFound
	}
	return p, err
}

// SavePreset inserts or updates a preset, assigning an id when it has none. The
// thumbnail file name is owned by SetThumbnail and left untouched on update.
func (s *Store) SavePreset(ctx context.Context, p preset.Preset) (preset.Preset, error) {
	if err := p.Validate(); err != nil {
		return preset.Preset{}, err
	}
	now := s.now().Unix()
	if p.ID == "" {
		p.ID = newID()
		_, err := s.db.ExecContext(ctx,
			"INSERT INTO presets (id, name, title_template, description, privacy, stream_id, weekday, time_of_day, thumbnail_file, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			p.ID, p.Name, p.TitleTemplate, p.Description, p.Privacy, p.StreamID, int(p.Weekday), p.TimeOfDay, p.ThumbnailFile, now, now)
		return p, err
	}
	if !validID(p.ID) {
		return preset.Preset{}, preset.ErrNotFound
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE presets SET name = ?, title_template = ?, description = ?, privacy = ?, stream_id = ?, weekday = ?, time_of_day = ?, updated_at = ? WHERE id = ?",
		p.Name, p.TitleTemplate, p.Description, p.Privacy, p.StreamID, int(p.Weekday), p.TimeOfDay, now, p.ID)
	if err != nil {
		return preset.Preset{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return preset.Preset{}, preset.ErrNotFound
	}
	return s.GetPreset(ctx, p.ID)
}

// SetThumbnail stores an image for the preset and records its file name; the new
// file is written before the old one is removed so a failure leaves the old image.
func (s *Store) SetThumbnail(ctx context.Context, id, ext string, image []byte) (preset.Preset, error) {
	p, err := s.GetPreset(ctx, id)
	if err != nil {
		return preset.Preset{}, err
	}
	file := id + "-" + newID() + ext
	if err := atomicfile.Write(s.imagePath(file), image, 0o600); err != nil {
		return preset.Preset{}, err
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE presets SET thumbnail_file = ?, updated_at = ? WHERE id = ?", file, s.now().Unix(), id); err != nil {
		_ = os.Remove(s.imagePath(file))
		return preset.Preset{}, err
	}
	if p.ThumbnailFile != "" {
		_ = os.Remove(s.imagePath(p.ThumbnailFile))
	}
	return s.GetPreset(ctx, id)
}

// Thumbnail returns the preset's image and content type; ok is false when none is set.
func (s *Store) Thumbnail(ctx context.Context, id string) (image []byte, contentType string, ok bool, err error) {
	p, err := s.GetPreset(ctx, id)
	if err != nil || p.ThumbnailFile == "" {
		return nil, "", false, err
	}
	image, err = os.ReadFile(s.imagePath(p.ThumbnailFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	return image, preset.ContentType(p.ThumbnailFile), true, nil
}

// DuplicatePreset copies a preset and its image under a new id, named "<name> (copy)".
func (s *Store) DuplicatePreset(ctx context.Context, id string) (preset.Preset, error) {
	p, err := s.GetPreset(ctx, id)
	if err != nil {
		return preset.Preset{}, err
	}
	image, _, hasImage, err := s.Thumbnail(ctx, id)
	if err != nil {
		return preset.Preset{}, err
	}
	ext := filepath.Ext(p.ThumbnailFile)
	p.ID, p.ThumbnailFile = "", ""
	p.Name += " (copy)"
	copied, err := s.SavePreset(ctx, p)
	if err != nil {
		return preset.Preset{}, err
	}
	if hasImage {
		return s.SetThumbnail(ctx, copied.ID, ext, image)
	}
	return copied, nil
}

// DeletePreset removes a preset and its image.
func (s *Store) DeletePreset(ctx context.Context, id string) error {
	p, err := s.GetPreset(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM presets WHERE id = ?", id); err != nil {
		return err
	}
	if p.ThumbnailFile != "" {
		_ = os.Remove(s.imagePath(p.ThumbnailFile))
	}
	return nil
}

// imagePath confines file access to the images directory whatever the stored name.
func (s *Store) imagePath(name string) string {
	return filepath.Join(s.images, filepath.Base(name))
}

func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// validID guards ids supplied over HTTP before they reach SQL or the filesystem.
func validID(id string) bool {
	return id != "" && strings.Trim(id, "0123456789abcdef") == ""
}
