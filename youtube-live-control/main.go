// Command youtube-live-control is the Home Assistant add-on binary: it exposes one
// YouTube live-broadcast control panel (selector, details, transitions) as MQTT
// entities and serves the ingress web UI for the one-time Google consent.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/web"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

var version = "dev"

const (
	supportURL      = "https://github.com/jacobgad/youtube-live-control"
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load(ctx)
	if err != nil {
		newLogger(slog.LevelInfo).Error("config_invalid", "detail", err.Error())
		return err
	}
	log := newLogger(cfg.Options.LogLevel)

	auth := youtube.NewAuth(cfg.Options.GoogleClientID, cfg.Options.GoogleClientSecret, cfg.TokenPath, log)
	if err := auth.Load(); err != nil {
		log.Error("token_load_failed", "error", err.Error())
		return err
	}
	if !auth.Configured() {
		log.Warn("oauth_client_missing", "detail", "set google_client_id and google_client_secret, then follow the web UI")
	}

	conn, err := mqtt.Connect(ctx, mqtt.PahoOptions{
		Settings: cfg.MQTT,
		ClientID: fmt.Sprintf("youtube-live-control-%d", os.Getpid()),
		Will:     mqtt.Will{Topic: mqtt.ControllerAvailability, Payload: mqtt.PayloadOffline},
		Log:      log,
	})
	if err != nil {
		log.Error("mqtt_setup_failed", "error", err.Error())
		return err
	}

	ctrl := controller.New(controller.Deps{
		YouTube:      youtube.NewClient(auth, log),
		Auth:         auth,
		MQTT:         conn,
		Options:      cfg.Options,
		SettingsPath: cfg.SettingsPath,
		Log:          log,
		Origin:       mqtt.Origin{Version: version, SupportURL: supportURL},
	})

	webErr := make(chan error, 1)
	go func() { webErr <- web.New(auth, cfg.Options, log).Run(ctx) }()

	if err := ctrl.Start(ctx); err != nil {
		log.Error("startup_failed", "error", err.Error())
		shutdown(ctrl)
		return err
	}

	select {
	case <-ctx.Done():
		log.Info("shutdown_requested")
	case err := <-webErr:
		if err != nil {
			log.Error("web_server_failed", "error", err.Error())
			shutdown(ctrl)
			return err
		}
	}
	shutdown(ctrl)
	return nil
}

func shutdown(ctrl *controller.Controller) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	ctrl.Stop(ctx)
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
