package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
)

// tunables are the runtime-adjustable settings behind the configuration number
// entities, persisted in /data so they survive restarts without living in the
// add-on options (which would need a restart to change).
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

// loadTunables overlays the persisted settings onto the defaults, clamping each
// value to its entity's range so a hand-edited file cannot stall the poll loops.
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
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// parseIntPayload accepts integers and integer-valued decimals such as "5.0",
// which Home Assistant may send for a number entity.
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
