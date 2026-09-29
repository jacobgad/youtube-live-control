package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseOptionsDefaults(t *testing.T) {
	opts, err := ParseOptions([]byte(`{"google_client_id":"id","google_client_secret":"secret"}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.GoogleClientID != "id" || opts.GoogleClientSecret != "secret" {
		t.Fatalf("client not parsed: %+v", opts)
	}
	if opts.Privacy != "public" {
		t.Fatalf("privacy default = %q", opts.Privacy)
	}
	if opts.ListPollInterval != 5*time.Minute || opts.FastPollInterval != 3*time.Second || opts.FastModeDuration != 5*time.Minute || opts.LivePollInterval != time.Minute || opts.IdlePollInterval != 10*time.Minute {
		t.Fatalf("interval defaults = %+v", opts)
	}
	if opts.ThumbnailsDir != "/media/youtube-live-control" {
		t.Fatalf("thumbnails default = %q", opts.ThumbnailsDir)
	}
	if opts.LogLevel != slog.LevelInfo {
		t.Fatalf("log level default = %v", opts.LogLevel)
	}
}

func TestParseOptionsEmptyClientAllowed(t *testing.T) {
	opts, err := ParseOptions([]byte(`{}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.GoogleClientID != "" {
		t.Fatalf("client id = %q", opts.GoogleClientID)
	}
}

func TestParseOptionsIntervals(t *testing.T) {
	opts, err := ParseOptions([]byte(`{"list_poll_minutes":2,"fast_poll_seconds":5,"fast_mode_minutes":3,"live_poll_seconds":30,"idle_poll_minutes":15}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.ListPollInterval != 2*time.Minute || opts.FastPollInterval != 5*time.Second || opts.FastModeDuration != 3*time.Minute || opts.LivePollInterval != 30*time.Second || opts.IdlePollInterval != 15*time.Minute {
		t.Fatalf("intervals = %+v", opts)
	}
}

func TestParseOptionsExternalURLTrimmed(t *testing.T) {
	opts, err := ParseOptions([]byte(`{"external_url":" http://ha.local:8098/ "}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.ExternalURL != "http://ha.local:8098" {
		t.Fatalf("external url = %q", opts.ExternalURL)
	}
}

func TestParseOptionsRejectsBadValues(t *testing.T) {
	for name, payload := range map[string]string{
		"privacy":   `{"privacy":"secret"}`,
		"fast_low":  `{"fast_poll_seconds":0}`,
		"live_high": `{"live_poll_seconds":601}`,
		"log_level": `{"log_level":"loud"}`,
		"not_json":  `{`,
	} {
		if _, err := ParseOptions([]byte(payload)); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}

func TestMQTTFromEnv(t *testing.T) {
	env := map[string]string{"MQTT_HOST": "core-mosquitto", "MQTT_PORT": "8883", "MQTT_SSL": "true", "MQTT_USERNAME": "u", "MQTT_PASSWORD": "p"}
	m, err := MQTTFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("MQTTFromEnv: %v", err)
	}
	if m.Host != "core-mosquitto" || m.Port != 8883 || !m.TLS || m.Username != "u" || m.Password != "p" {
		t.Fatalf("MQTT = %+v", m)
	}
	if _, err := MQTTFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("missing host accepted")
	}
}

func TestOptionsRenderingHidesClientSecret(t *testing.T) {
	opts := Options{GoogleClientID: "id", GoogleClientSecret: "GOCSPX-hunter2", Privacy: "public"}
	for _, rendered := range []string{opts.String(), opts.GoString(), fmt.Sprintf("%v %+v %#v", opts, opts, opts), opts.LogValue().String()} {
		if strings.Contains(rendered, "hunter2") {
			t.Fatalf("secret leaked: %s", rendered)
		}
	}
}

func TestMQTTStringHidesPassword(t *testing.T) {
	m := MQTT{Host: "h", Port: 1883, Username: "u", Password: "hunter2"}
	for _, rendered := range []string{m.String(), m.GoString()} {
		if want := "mqtt://u@h:1883 tls=false"; rendered != want {
			t.Fatalf("rendered = %q, want %q", rendered, want)
		}
	}
}
