// Package config loads add-on options and the MQTT broker details.
package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Options are the validated add-on options.
type Options struct {
	GoogleClientID     string
	GoogleClientSecret string
	ExternalURL        string
	ListPollInterval   time.Duration
	FastPollInterval   time.Duration
	FastModeDuration   time.Duration
	LivePollInterval   time.Duration
	IdlePollInterval   time.Duration
	LogLevel           slog.Level
}

// String omits the client secret so no fmt path can leak it.
func (o Options) String() string {
	return fmt.Sprintf("Options{clientID=%s externalURL=%s listPoll=%s fastPoll=%s fastMode=%s livePoll=%s idlePoll=%s logLevel=%s}",
		o.GoogleClientID, o.ExternalURL, o.ListPollInterval, o.FastPollInterval, o.FastModeDuration, o.LivePollInterval, o.IdlePollInterval, o.LogLevel)
}

// GoString mirrors String for %#v, which bypasses Stringer.
func (o Options) GoString() string { return o.String() }

// LogValue mirrors String for slog.
func (o Options) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("clientID", o.GoogleClientID),
		slog.String("externalURL", o.ExternalURL),
		slog.Duration("listPoll", o.ListPollInterval),
		slog.Duration("fastPoll", o.FastPollInterval),
		slog.Duration("fastMode", o.FastModeDuration),
		slog.Duration("livePoll", o.LivePollInterval),
		slog.Duration("idlePoll", o.IdlePollInterval),
		slog.String("logLevel", o.LogLevel.String()),
	)
}

// MQTT is how to reach the broker.
type MQTT struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      bool
}

// String omits the password so no fmt path can leak it.
func (m MQTT) String() string {
	return fmt.Sprintf("mqtt://%s@%s:%d tls=%v", m.Username, m.Host, m.Port, m.TLS)
}

// GoString mirrors String for %#v, which bypasses Stringer.
func (m MQTT) GoString() string { return m.String() }

// LogValue mirrors String for slog.
func (m MQTT) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", m.Host),
		slog.Int("port", m.Port),
		slog.String("username", m.Username),
		slog.Bool("tls", m.TLS),
	)
}

// Config is everything the binary needs to start.
type Config struct {
	Options      Options
	MQTT         MQTT
	DatabasePath string
	ImagesDir    string
}

type rawOptions struct {
	GoogleClientID     *string `json:"google_client_id"`
	GoogleClientSecret *string `json:"google_client_secret"`
	ExternalURL        *string `json:"external_url"`
	ListPollMinutes    *int    `json:"list_poll_minutes"`
	FastPollSeconds    *int    `json:"fast_poll_seconds"`
	FastModeMinutes    *int    `json:"fast_mode_minutes"`
	LivePollSeconds    *int    `json:"live_poll_seconds"`
	IdlePollMinutes    *int    `json:"idle_poll_minutes"`
	LogLevel           *string `json:"log_level"`
}

type intervalOption struct {
	name  string
	raw   *int
	min   int
	max   int
	unit  time.Duration
	value *time.Duration
}

// ParseOptions validates options.json and applies defaults. An empty Google client is
// allowed so the add-on can start and walk the user through setup instead of crash-looping.
func ParseOptions(data []byte) (Options, error) {
	var raw rawOptions
	if err := json.Unmarshal(data, &raw); err != nil {
		return Options{}, fmt.Errorf("options are not valid JSON: %w", err)
	}
	opts := Options{
		ListPollInterval: 5 * time.Minute,
		FastPollInterval: 3 * time.Second,
		FastModeDuration: 5 * time.Minute,
		LivePollInterval: time.Minute,
		IdlePollInterval: 10 * time.Minute,
		LogLevel:         slog.LevelInfo,
	}
	if raw.GoogleClientID != nil {
		opts.GoogleClientID = strings.TrimSpace(*raw.GoogleClientID)
	}
	if raw.GoogleClientSecret != nil {
		opts.GoogleClientSecret = strings.TrimSpace(*raw.GoogleClientSecret)
	}
	if raw.ExternalURL != nil {
		opts.ExternalURL = strings.TrimRight(strings.TrimSpace(*raw.ExternalURL), "/")
	}
	intervals := []intervalOption{
		{"list_poll_minutes", raw.ListPollMinutes, 1, 60, time.Minute, &opts.ListPollInterval},
		{"fast_poll_seconds", raw.FastPollSeconds, 1, 30, time.Second, &opts.FastPollInterval},
		{"fast_mode_minutes", raw.FastModeMinutes, 1, 60, time.Minute, &opts.FastModeDuration},
		{"live_poll_seconds", raw.LivePollSeconds, 15, 600, time.Second, &opts.LivePollInterval},
		{"idle_poll_minutes", raw.IdlePollMinutes, 1, 60, time.Minute, &opts.IdlePollInterval},
	}
	for _, opt := range intervals {
		if opt.raw == nil {
			continue
		}
		if v := *opt.raw; v < opt.min || v > opt.max {
			return Options{}, fmt.Errorf("%s must be between %d and %d", opt.name, opt.min, opt.max)
		}
		*opt.value = time.Duration(*opt.raw) * opt.unit
	}
	if raw.LogLevel != nil {
		if err := opts.LogLevel.UnmarshalText([]byte(*raw.LogLevel)); err != nil {
			return Options{}, errors.New("log_level must be one of debug, info, warn, error")
		}
	}
	return opts, nil
}

// MQTTFromEnv reads MQTT_HOST, MQTT_PORT, MQTT_USERNAME, MQTT_PASSWORD and MQTT_SSL.
func MQTTFromEnv(getenv func(string) string) (MQTT, error) {
	host := getenv("MQTT_HOST")
	if host == "" {
		return MQTT{}, errors.New("MQTT_HOST is required")
	}
	m := MQTT{Host: host, Port: 1883, Username: getenv("MQTT_USERNAME"), Password: getenv("MQTT_PASSWORD")}
	if p := getenv("MQTT_PORT"); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return MQTT{}, fmt.Errorf("MQTT_PORT %q is not a valid port", p)
		}
		m.Port = port
	}
	switch strings.ToLower(getenv("MQTT_SSL")) {
	case "true", "1", "yes":
		m.TLS = true
	}
	return m, nil
}

const supervisorServicesURL = "http://supervisor/services/mqtt"

// MQTTFromSupervisor fetches the broker from the Supervisor services API.
func MQTTFromSupervisor(ctx context.Context, token string, client *http.Client) (MQTT, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, supervisorServicesURL, nil)
	if err != nil {
		return MQTT{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return MQTT{}, fmt.Errorf("supervisor request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return MQTT{}, fmt.Errorf("supervisor returned HTTP %d for the MQTT service", resp.StatusCode)
	}
	var payload struct {
		Result string `json:"result"`
		Data   struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
			SSL      bool   `json:"ssl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Result != "ok" || payload.Data.Host == "" {
		return MQTT{}, errors.New("supervisor did not return a usable MQTT service; is the Mosquitto broker add-on running?")
	}
	return MQTT{Host: payload.Data.Host, Port: payload.Data.Port, Username: payload.Data.Username, Password: payload.Data.Password, TLS: payload.Data.SSL}, nil
}

// Load reads options.json and resolves MQTT settings, preferring MQTT_HOST when set.
func Load(ctx context.Context) (Config, error) {
	optionsPath := envOr("YLC_OPTIONS_PATH", "/data/options.json")
	data, err := os.ReadFile(optionsPath) //nolint:gosec // path is fixed by the add-on or set by the operator's own environment
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", optionsPath, err)
	}
	opts, err := ParseOptions(data)
	if err != nil {
		return Config{}, err
	}
	var mqtt MQTT
	if os.Getenv("MQTT_HOST") != "" {
		mqtt, err = MQTTFromEnv(os.Getenv)
	} else if token := os.Getenv("SUPERVISOR_TOKEN"); token != "" {
		mqtt, err = MQTTFromSupervisor(ctx, token, &http.Client{Timeout: 10 * time.Second})
	} else {
		err = errors.New("no MQTT configuration: set MQTT_HOST or run under the Home Assistant Supervisor")
	}
	if err != nil {
		return Config{}, err
	}
	return Config{
		Options:      opts,
		MQTT:         mqtt,
		DatabasePath: envOr("YLC_DATABASE_PATH", "/data/ylc.sqlite"),
		ImagesDir:    envOr("YLC_IMAGES_DIR", "/data/images"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
