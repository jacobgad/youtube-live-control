// Package preset stores the reusable broadcast templates (title pattern, description,
// privacy, stream key, thumbnail, usual day and time) a producer picks from when
// scheduling a new stream. Presets live under /data so they survive updates.
package preset

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/atomicfile"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// The dashboard's Time select spans these half-hour slots; a preset outside them
// would default to a time the panel cannot show.
const (
	FirstSlot = 6 * time.Hour
	LastSlot  = 23*time.Hour + 30*time.Minute
	SlotStep  = 30 * time.Minute
)

// ValidSlot reports whether a "15:04" time is a half-hour within the dashboard window.
func ValidSlot(hhmm string) bool {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return false
	}
	d := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	return d >= FirstSlot && d <= LastSlot && d%SlotStep == 0
}

// DatePlaceholder in a title template is replaced by the scheduled date.
const DatePlaceholder = "{date}"

const dateFormat = "2 Jan 2006"

// Preset is one template. ThumbnailFile is the image's file name inside the store
// directory, empty when the preset has no thumbnail.
type Preset struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	TitleTemplate string       `json:"title_template"`
	Description   string       `json:"description"`
	Privacy       string       `json:"privacy"`
	StreamID      string       `json:"stream_id"`
	Weekday       time.Weekday `json:"weekday"`
	TimeOfDay     string       `json:"time_of_day"`
	ThumbnailFile string       `json:"thumbnail_file,omitempty"`
}

// Validate reports the first problem with a preset as a user-facing message.
func (p Preset) Validate() error {
	switch {
	case strings.TrimSpace(p.Name) == "":
		return errors.New("name is required")
	case strings.TrimSpace(p.TitleTemplate) == "":
		return errors.New("title is required")
	case !youtube.ValidPrivacy(p.Privacy):
		return errors.New("privacy must be public, unlisted or private")
	case p.StreamID == "":
		return errors.New("a stream key is required")
	case p.Weekday < time.Sunday || p.Weekday > time.Saturday:
		return errors.New("weekday is out of range")
	}
	if !ValidSlot(p.TimeOfDay) {
		return errors.New("time must be on the hour or half hour between 06:00 and 23:30")
	}
	return nil
}

// Title renders the template for a broadcast on the given date.
func (p Preset) Title(start time.Time) string {
	return strings.ReplaceAll(p.TitleTemplate, DatePlaceholder, start.Local().Format(dateFormat))
}

// NextStart is the next occurrence of the preset's weekday and time after now.
func (p Preset) NextStart(now time.Time) time.Time {
	tod, err := time.Parse("15:04", p.TimeOfDay)
	if err != nil {
		tod = time.Date(0, 1, 1, 9, 0, 0, 0, time.UTC)
	}
	now = now.Local()
	candidate := time.Date(now.Year(), now.Month(), now.Day(), tod.Hour(), tod.Minute(), 0, 0, now.Location())
	days := (int(p.Weekday) - int(now.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, days)
	if !candidate.After(now) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

// Store is the on-disk preset collection: one <id>.json per preset plus its image.
type Store struct {
	dir string
}

// Open ensures the directory exists.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("presets dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// List returns every preset sorted by name.
func (s *Store) List() ([]Preset, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var presets []Preset
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		p, err := s.Get(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		presets = append(presets, p)
	}
	slices.SortFunc(presets, func(a, b Preset) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return presets, nil
}

// ErrNotFound is returned for an unknown preset id.
var ErrNotFound = errors.New("preset not found")

// Get loads one preset.
func (s *Store) Get(id string) (Preset, error) {
	if !validID(id) {
		return Preset{}, ErrNotFound
	}
	data, err := os.ReadFile(s.path(id + ".json"))
	if errors.Is(err, os.ErrNotExist) {
		return Preset{}, ErrNotFound
	}
	if err != nil {
		return Preset{}, err
	}
	var p Preset
	if err := json.Unmarshal(data, &p); err != nil {
		return Preset{}, fmt.Errorf("preset %s: %w", id, err)
	}
	p.ID = id
	return p, nil
}

// Save writes a preset, assigning an id when it has none.
func (s *Store) Save(p Preset) (Preset, error) {
	if err := p.Validate(); err != nil {
		return Preset{}, err
	}
	if p.ID == "" {
		p.ID = newID()
	} else if !validID(p.ID) {
		return Preset{}, ErrNotFound
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return Preset{}, err
	}
	if err := atomicfile.Write(s.path(p.ID+".json"), data, 0o600); err != nil {
		return Preset{}, err
	}
	return p, nil
}

// SetThumbnail stores an image for the preset and records its file name.
func (s *Store) SetThumbnail(id string, ext string, image []byte) (Preset, error) {
	p, err := s.Get(id)
	if err != nil {
		return Preset{}, err
	}
	previous := p.ThumbnailFile
	p.ThumbnailFile = id + ext
	if err := atomicfile.Write(s.path(p.ThumbnailFile), image, 0o600); err != nil {
		return Preset{}, err
	}
	if previous != "" && previous != p.ThumbnailFile {
		_ = os.Remove(s.path(previous))
	}
	return s.Save(p)
}

// Thumbnail returns the preset's image bytes and content type; ok is false when none is set.
func (s *Store) Thumbnail(id string) (image []byte, contentType string, ok bool, err error) {
	p, err := s.Get(id)
	if err != nil || p.ThumbnailFile == "" {
		return nil, "", false, err
	}
	image, err = os.ReadFile(s.path(p.ThumbnailFile))
	if err != nil {
		return nil, "", false, err
	}
	return image, ContentType(p.ThumbnailFile), true, nil
}

// Delete removes a preset and its image.
func (s *Store) Delete(id string) error {
	p, err := s.Get(id)
	if err != nil {
		return err
	}
	if p.ThumbnailFile != "" {
		_ = os.Remove(s.path(p.ThumbnailFile))
	}
	return os.Remove(s.path(id + ".json"))
}

// ContentType maps a thumbnail file name to its MIME type.
func ContentType(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// validID guards the file paths derived from an id supplied over HTTP.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
