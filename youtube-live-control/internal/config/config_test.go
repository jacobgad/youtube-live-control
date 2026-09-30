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
	if opts.IdleRefresh != 10*time.Minute || opts.LiveRefresh != time.Minute || opts.FastRefresh != 3*time.Second || opts.FastRefreshDuration != 5*time.Minute {
		t.Fatalf("interval defaults = %+v", opts)
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
	opts, err := ParseOptions([]byte(`{"refresh_idle_seconds":900,"refresh_live_seconds":30,"refresh_fast_seconds":5,"fast_refresh_duration_seconds":180}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.IdleRefresh != 15*time.Minute || opts.LiveRefresh != 30*time.Second || opts.FastRefresh != 5*time.Second || opts.FastRefreshDuration != 3*time.Minute {
		t.Fatalf("intervals = %+v", opts)
	}
}

func TestParseOptionsRedirectBaseURLTrimmed(t *testing.T) {
	opts, err := ParseOptions([]byte(`{"oauth_redirect_base_url":" https://ylc.example.org/ "}`))
	if err != nil {
		t.Fatalf("ParseOptions: %v", err)
	}
	if opts.OAuthRedirectBaseURL != "https://ylc.example.org" {
		t.Fatalf("redirect base url = %q", opts.OAuthRedirectBaseURL)
	}
}

func TestParseOptionsRejectsBadValues(t *testing.T) {
	for name, payload := range map[string]string{
		"idle_low":  `{"refresh_idle_seconds":30}`,
		"live_high": `{"refresh_live_seconds":601}`,
		"fast_high": `{"refresh_fast_seconds":31}`,
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
	opts := Options{GoogleClientID: "id", GoogleClientSecret: "GOCSPX-hunter2"}
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
