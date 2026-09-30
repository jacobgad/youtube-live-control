// Package preset is the reusable broadcast template (title pattern, description,
// privacy, stream key, thumbnail, usual day and time) a producer schedules from.
package preset

import (
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// DatePlaceholder in a title template is replaced by the scheduled date.
const DatePlaceholder = "{date}"

const dateFormat = "2 Jan 2006"

// Preset is one template. ThumbnailFile is the image's file name inside the images
// directory, empty when the preset has no thumbnail.
type Preset struct {
	ID            string
	Name          string
	TitleTemplate string
	Description   string
	Privacy       string
	StreamID      string
	Weekday       time.Weekday
	TimeOfDay     string
	ThumbnailFile string
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
	if _, err := time.Parse("15:04", p.TimeOfDay); err != nil {
		return errors.New("time must be HH:MM")
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

// ErrNotFound is returned for an unknown preset id.
var ErrNotFound = errors.New("preset not found")

// ContentType maps a thumbnail file name to its MIME type.
func ContentType(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}
