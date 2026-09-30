// Package store persists presets, image records, settings and the refresh token in
// one SQLite file, with image files in one directory beside it.
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

// KeyRefreshToken is the settings key holding the Google refresh token.
const KeyRefreshToken = "refresh_token"

// ErrImageInUse is returned when deleting an image a preset still references.
var ErrImageInUse = errors.New("image is used by a preset")

// ErrImageNotFound is returned for an unknown image id.
var ErrImageNotFound = errors.New("image not found")

const schemaVersion = 4

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
	`CREATE TABLE images (
		id TEXT PRIMARY KEY NOT NULL,
		file TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		size INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	ALTER TABLE presets ADD COLUMN image_id TEXT NOT NULL DEFAULT '';
	INSERT INTO images (id, file, name, size, created_at)
		SELECT lower(hex(randomblob(6))), thumbnail_file, name || ' thumbnail', 0, created_at
		FROM presets WHERE thumbnail_file != '';
	UPDATE presets SET image_id = (SELECT id FROM images WHERE images.file = presets.thumbnail_file)
		WHERE thumbnail_file != '';
	ALTER TABLE presets DROP COLUMN thumbnail_file`,
	`ALTER TABLE images DROP COLUMN name`,
	`ALTER TABLE presets ADD COLUMN category_id TEXT NOT NULL DEFAULT ''`,
}

// Image is one library entry.
type Image struct {
	ID     string
	File   string
	Size   int64
	UsedBy int
}

// ContentType is the MIME type implied by the file name.
func (i Image) ContentType() string { return contentType(i.File) }

// Store is the open database plus the images directory.
type Store struct {
	db     *sql.DB
	images string
	now    func() time.Time
}

// Open opens or creates the database and migrates it to the current schema.
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
	for _, pragma := range []string{"PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 5000"} {
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

// DeleteSetting removes a settings value; a missing key is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
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

const presetColumns = "id, name, title_template, description, privacy, stream_id, weekday, time_of_day, image_id, category_id"

func scanPreset(row interface{ Scan(...any) error }) (preset.Preset, error) {
	var p preset.Preset
	var weekday int
	err := row.Scan(&p.ID, &p.Name, &p.TitleTemplate, &p.Description, &p.Privacy, &p.StreamID, &weekday, &p.TimeOfDay, &p.ImageID, &p.CategoryID)
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

// SavePreset inserts (empty ID) or updates a preset.
func (s *Store) SavePreset(ctx context.Context, p preset.Preset) (preset.Preset, error) {
	if err := p.Validate(); err != nil {
		return preset.Preset{}, err
	}
	if p.ImageID != "" {
		if _, err := s.GetImage(ctx, p.ImageID); err != nil {
			return preset.Preset{}, err
		}
	}
	now := s.now().Unix()
	if p.ID == "" {
		p.ID = newID()
		_, err := s.db.ExecContext(ctx,
			"INSERT INTO presets (id, name, title_template, description, privacy, stream_id, weekday, time_of_day, image_id, category_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			p.ID, p.Name, p.TitleTemplate, p.Description, p.Privacy, p.StreamID, int(p.Weekday), p.TimeOfDay, p.ImageID, p.CategoryID, now, now)
		return p, err
	}
	if !validID(p.ID) {
		return preset.Preset{}, preset.ErrNotFound
	}
	res, err := s.db.ExecContext(ctx,
		"UPDATE presets SET name = ?, title_template = ?, description = ?, privacy = ?, stream_id = ?, weekday = ?, time_of_day = ?, image_id = ?, category_id = ?, updated_at = ? WHERE id = ?",
		p.Name, p.TitleTemplate, p.Description, p.Privacy, p.StreamID, int(p.Weekday), p.TimeOfDay, p.ImageID, p.CategoryID, now, p.ID)
	if err != nil {
		return preset.Preset{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return preset.Preset{}, preset.ErrNotFound
	}
	return s.GetPreset(ctx, p.ID)
}

// DuplicatePreset copies a preset as "<name> (copy)", sharing its image.
func (s *Store) DuplicatePreset(ctx context.Context, id string) (preset.Preset, error) {
	p, err := s.GetPreset(ctx, id)
	if err != nil {
		return preset.Preset{}, err
	}
	p.ID = ""
	p.Name += " (copy)"
	return s.SavePreset(ctx, p)
}

// DeletePreset removes a preset; its image stays in the library.
func (s *Store) DeletePreset(ctx context.Context, id string) error {
	if !validID(id) {
		return preset.ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, "DELETE FROM presets WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return preset.ErrNotFound
	}
	return nil
}

const imageColumns = "i.id, i.file, i.size, (SELECT count(*) FROM presets p WHERE p.image_id = i.id)"

func scanImage(row interface{ Scan(...any) error }) (Image, error) {
	var img Image
	err := row.Scan(&img.ID, &img.File, &img.Size, &img.UsedBy)
	return img, err
}

// ListImages returns the library, newest first.
func (s *Store) ListImages(ctx context.Context) ([]Image, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+imageColumns+" FROM images i ORDER BY i.created_at DESC, i.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// GetImage loads one library entry.
func (s *Store) GetImage(ctx context.Context, id string) (Image, error) {
	if !validID(id) {
		return Image{}, ErrImageNotFound
	}
	img, err := scanImage(s.db.QueryRowContext(ctx, "SELECT "+imageColumns+" FROM images i WHERE i.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Image{}, ErrImageNotFound
	}
	return img, err
}

// AddImage stores an image; the file lands before the row so a failed insert dangles nothing.
func (s *Store) AddImage(ctx context.Context, imageContentType string, data []byte) (Image, error) {
	id := newID()
	file := id + extensionFor(imageContentType)
	if err := atomicfile.Write(s.imagePath(file), data, 0o600); err != nil {
		return Image{}, err
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO images (id, file, size, created_at) VALUES (?, ?, ?, ?)", id, file, len(data), s.now().Unix())
	if err != nil {
		_ = os.Remove(s.imagePath(file))
		return Image{}, err
	}
	return s.GetImage(ctx, id)
}

// ImageBytes reads an image's file.
func (s *Store) ImageBytes(ctx context.Context, id string) (data []byte, imageContentType string, err error) {
	img, err := s.GetImage(ctx, id)
	if err != nil {
		return nil, "", err
	}
	data, err = os.ReadFile(s.imagePath(img.File))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrImageNotFound
	}
	if err != nil {
		return nil, "", err
	}
	return data, img.ContentType(), nil
}

// DeleteImage removes an unused image and its file.
func (s *Store) DeleteImage(ctx context.Context, id string) error {
	img, err := s.GetImage(ctx, id)
	if err != nil {
		return err
	}
	if img.UsedBy > 0 {
		return ErrImageInUse
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM images WHERE id = ?", id); err != nil {
		return err
	}
	_ = os.Remove(s.imagePath(img.File))
	return nil
}

// filepath.Base confines access to the images directory whatever the stored name.
func (s *Store) imagePath(name string) string {
	return filepath.Join(s.images, filepath.Base(name))
}

func extensionFor(imageContentType string) string {
	if imageContentType == "image/png" {
		return ".png"
	}
	return ".jpg"
}

func contentType(file string) string {
	if strings.EqualFold(filepath.Ext(file), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}

func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Ids arrive over HTTP; hex-only keeps them out of SQL and paths.
func validID(id string) bool {
	return id != "" && strings.Trim(id, "0123456789abcdef") == ""
}
