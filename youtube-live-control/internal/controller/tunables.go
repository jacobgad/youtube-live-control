package controller

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/atomicfile"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
)

// Persisted in /data rather than the add-on options so a change needs no restart.
type tunables struct {
	ListPollMinutes int `json:"list_poll_minutes"`
	FastPollSeconds int `json:"fast_poll_seconds"`
	FastModeMinutes int `json:"fast_mode_minutes"`
	LivePollSeconds int `json:"live_poll_seconds"`
	IdlePollMinutes int `json:"idle_poll_minutes"`
}

var defaultTunables = tunables{
	ListPollMinutes: 5,
	FastPollSeconds: 3,
	FastModeMinutes: 5,
	LivePollSeconds: 60,
	IdlePollMinutes: 10,
}

var tunableFields = map[string]func(*tunables) *int{
	"list_poll_minutes": func(t *tunables) *int { return &t.ListPollMinutes },
	"fast_poll_seconds": func(t *tunables) *int { return &t.FastPollSeconds },
	"fast_mode_minutes": func(t *tunables) *int { return &t.FastModeMinutes },
	"live_poll_seconds": func(t *tunables) *int { return &t.LivePollSeconds },
	"idle_poll_minutes": func(t *tunables) *int { return &t.IdlePollMinutes },
}

func tunableSpec(object string) (mqtt.NumberSpec, bool) {
	for _, spec := range mqtt.TunableSpecs {
		if spec.Object == object {
			return spec, true
		}
	}
	return mqtt.NumberSpec{}, false
}

func (t tunables) listPoll() time.Duration   { return time.Duration(t.ListPollMinutes) * time.Minute }
func (t tunables) fastPoll() time.Duration   { return time.Duration(t.FastPollSeconds) * time.Second }
func (t tunables) fastWindow() time.Duration { return time.Duration(t.FastModeMinutes) * time.Minute }
func (t tunables) livePoll() time.Duration   { return time.Duration(t.LivePollSeconds) * time.Second }
func (t tunables) idlePoll() time.Duration   { return time.Duration(t.IdlePollMinutes) * time.Minute }

// Clamped to the entity ranges so a hand-edited file cannot stall the poll loops.
func loadTunables(path string, log *slog.Logger) tunables {
	t := defaultTunables
	data, err := os.ReadFile(path) //nolint:gosec // path is fixed by the add-on
	if errors.Is(err, os.ErrNotExist) {
		return t
	}
	if err != nil {
		log.Warn("settings_read_failed", "path", path, "error", err.Error())
		return t
	}
	if err := json.Unmarshal(data, &t); err != nil {
		log.Warn("settings_parse_failed", "path", path, "error", err.Error())
		return defaultTunables
	}
	for _, spec := range mqtt.TunableSpecs {
		v := tunableFields[spec.Object](&t)
		clamped := min(max(*v, spec.Min), spec.Max)
		if clamped != *v {
			log.Warn("setting_clamped", "setting", spec.Object, "stored", *v, "used", clamped)
			*v = clamped
		}
	}
	return t
}

func (t tunables) save(path string) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, data, 0o600)
}

// Home Assistant may send "5.0" for a step-1 number.
func parseIntPayload(raw string) (int, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	n := int(value)
	if float64(n) != value {
		return 0, false
	}
	return n, true
}
