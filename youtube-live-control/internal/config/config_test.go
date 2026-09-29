package config

import (
	"log/slog"
	"testing"
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

func TestMQTTStringHidesPassword(t *testing.T) {
	m := MQTT{Host: "h", Port: 1883, Username: "u", Password: "hunter2"}
	for _, rendered := range []string{m.String(), m.GoString()} {
		if want := "mqtt://u@h:1883 tls=false"; rendered != want {
			t.Fatalf("rendered = %q, want %q", rendered, want)
		}
	}
}
