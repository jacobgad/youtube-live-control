// Package config loads add-on options from /data/options.json and the MQTT broker
// details from either the environment or the Home Assistant Supervisor.
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

// Options are the validated add-on options: infrastructure that needs a restart.
// Runtime behaviour (poll tiers, fast window, create lead) is tuned through the
// add-on's own MQTT number entities instead and persists in /data.
type Options struct {
	GoogleClientID     string
	GoogleClientSecret string
	ExternalURL        string
	Privacy            string
	ThumbnailsDir      string
	LogLevel           slog.Level
}

// MQTT is how to reach the broker.
type MQTT struct {
	Host     string
	Port     int
	Username string
	Password string
	TLS      bool
}

// String renders the broker address without the password, so formatting an MQTT
// value (or any struct containing one) can never leak the credential into logs.
func (m MQTT) String() string {
	return fmt.Sprintf("mqtt://%s@%s:%d tls=%v", m.Username, m.Host, m.Port, m.TLS)
}

// GoString mirrors String for %#v, which bypasses Stringer.
func (m MQTT) GoString() string { return m.String() }

// LogValue renders the broker details for slog without the password.
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
	TokenPath    string
	SettingsPath string
}

type rawOptions struct {
	GoogleClientID     *string `json:"google_client_id"`
	GoogleClientSecret *string `json:"google_client_secret"`
	ExternalURL        *string `json:"external_url"`
	Privacy            *string `json:"privacy"`
	ThumbnailsDir      *string `json:"thumbnails_dir"`
	LogLevel           *string `json:"log_level"`
}

// ParseOptions validates the JSON contents of options.json and applies defaults.
// An empty Google client is allowed so the add-on can start and walk the user
// through OAuth setup in the ingress UI instead of crash-looping.
func ParseOptions(data []byte) (Options, error) {
	var raw rawOptions
	if err := json.Unmarshal(data, &raw); err != nil {
		return Options{}, fmt.Errorf("options are not valid JSON: %w", err)
	}
	opts := Options{
		Privacy:       "public",
		ThumbnailsDir: "/media/youtube-live-control",
		LogLevel:      slog.LevelInfo,
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
	if raw.Privacy != nil {
		switch *raw.Privacy {
		case "public", "unlisted", "private":
			opts.Privacy = *raw.Privacy
		default:
			return Options{}, errors.New("privacy must be one of public, unlisted, private")
		}
	}
	if raw.ThumbnailsDir != nil && strings.TrimSpace(*raw.ThumbnailsDir) != "" {
		opts.ThumbnailsDir = strings.TrimSpace(*raw.ThumbnailsDir)
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

// MQTTFromSupervisor fetches the broker registered with the Supervisor services API,
// which is reachable without hassio_api once the add-on declares the mqtt service.
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
		TokenPath:    envOr("YLC_TOKEN_PATH", "/data/token.json"),
		SettingsPath: envOr("YLC_SETTINGS_PATH", "/data/settings.json"),
	}, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
