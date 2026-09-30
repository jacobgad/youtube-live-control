// Package web serves the ingress console — Google consent, the broadcast editor and
// preset management — plus the plain-port OAuth callback Google can redirect to.
package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/config"
	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// 8099 is the Supervisor's default ingress_port (the add-on linter forbids restating
// it in config.yaml); 8098 must match config.yaml's ports so Google can reach it.
const (
	ingressPort  = 8099
	callbackPort = 8098

	stateTTL        = 15 * time.Minute
	shutdownTimeout = 5 * time.Second
	requestTimeout  = 60 * time.Second
	// Form fields plus one thumbnail at YouTube's 2 MB limit, with headroom for encoding.
	maxRequestBytes = youtube.MaxThumbnailBytes + 512<<10
)

//go:embed templates/*.html
var templateFS embed.FS

// Server is the ingress UI plus the OAuth callback listener.
type Server struct {
	auth  *youtube.Auth
	ctrl  *controller.Controller
	store *store.Store
	opts  config.Options
	log   *slog.Logger
	pages map[string]*template.Template

	mu     sync.Mutex
	states map[string]stateEntry
}

type stateEntry struct {
	redirectURI string
	expires     time.Time
}

// New builds the server; Run starts it.
func New(auth *youtube.Auth, ctrl *controller.Controller, st *store.Store, opts config.Options, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		auth:   auth,
		ctrl:   ctrl,
		store:  st,
		opts:   opts,
		log:    log,
		pages:  parsePages(),
		states: map[string]stateEntry{},
	}
}

func parsePages() map[string]*template.Template {
	funcs := template.FuncMap{
		"localTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("Mon 2 Jan 2006 15:04")
		},
		"inputTime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.Local().Format("2006-01-02T15:04")
		},
		"weekdays": func() []time.Weekday {
			return []time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}
		},
		"keySuffix": func(key string) string {
			if len(key) <= 4 {
				return key
			}
			return "…" + key[len(key)-4:]
		},
	}
	layout := template.Must(template.New("layout").Funcs(funcs).ParseFS(templateFS, "templates/layout.html"))
	pages := map[string]*template.Template{}
	for _, name := range []string{"connection", "broadcasts", "broadcast_form", "presets", "preset_form"} {
		clone := template.Must(layout.Clone())
		pages[name] = template.Must(clone.ParseFS(templateFS, "templates/"+name+".html"))
	}
	return pages
}

// Run serves both listeners until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ingressMux := http.NewServeMux()
	ingressMux.HandleFunc("GET /{$}", s.handleHome)
	ingressMux.HandleFunc("GET /connection", s.handleConnection)
	ingressMux.HandleFunc("POST /manual", s.handleManual)
	ingressMux.HandleFunc("GET /broadcasts", s.requireAuth(s.handleBroadcasts))
	ingressMux.HandleFunc("GET /broadcasts/new", s.requireAuth(s.handleNewBroadcastForm))
	ingressMux.HandleFunc("POST /broadcasts/new", s.requireAuth(s.handleNewBroadcast))
	ingressMux.HandleFunc("GET /broadcasts/{id}", s.requireAuth(s.handleEditBroadcastForm))
	ingressMux.HandleFunc("POST /broadcasts/{id}", s.requireAuth(s.handleEditBroadcast))
	ingressMux.HandleFunc("POST /broadcasts/{id}/delete", s.requireAuth(s.handleDeleteBroadcast))
	ingressMux.HandleFunc("GET /presets", s.requireAuth(s.handlePresets))
	ingressMux.HandleFunc("GET /presets/new", s.requireAuth(s.handlePresetForm))
	ingressMux.HandleFunc("POST /presets/new", s.requireAuth(s.handleSavePreset))
	ingressMux.HandleFunc("GET /presets/{id}", s.requireAuth(s.handlePresetForm))
	ingressMux.HandleFunc("POST /presets/{id}", s.requireAuth(s.handleSavePreset))
	ingressMux.HandleFunc("POST /presets/{id}/delete", s.requireAuth(s.handleDeletePreset))
	ingressMux.HandleFunc("POST /presets/{id}/duplicate", s.requireAuth(s.handleDuplicatePreset))
	ingressMux.HandleFunc("GET /presets/{id}/thumbnail", s.handlePresetThumbnail)

	callbackMux := http.NewServeMux()
	callbackMux.HandleFunc("GET /oauth/callback", s.handleCallback)

	ingress := &http.Server{Addr: fmt.Sprintf(":%d", ingressPort), Handler: ingressMux, ReadHeaderTimeout: 10 * time.Second}
	callback := &http.Server{Addr: fmt.Sprintf(":%d", callbackPort), Handler: callbackMux, ReadHeaderTimeout: 10 * time.Second}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = ingress.Shutdown(shutdownCtx)
		_ = callback.Shutdown(shutdownCtx)
	}()

	errCh := make(chan error, 2)
	go func() { errCh <- serve(ingress, "ingress", s.log) }()
	go func() { errCh <- serve(callback, "oauth_callback", s.log) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func serve(srv *http.Server, name string, log *slog.Logger) error {
	log.Info("http_listening", "server", name, "addr", srv.Addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s server: %w", name, err)
}

// base is the ingress prefix Home Assistant proxies through; every link and redirect
// must carry it, since the add-on itself only ever sees root-relative paths.
func base(r *http.Request) string {
	return strings.TrimRight(r.Header.Get("X-Ingress-Path"), "/")
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path string, flash ...string) {
	target := base(r) + path
	if len(flash) == 2 && flash[1] != "" {
		target += "?" + url.Values{flash[0]: {flash[1]}}.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.Configured() || !s.auth.Authorized() {
			s.redirect(w, r, "/connection")
			return
		}
		next(w, r)
	}
}

type page struct {
	Base       string
	Title      string
	Tab        string
	Channel    string
	Authorized bool
	Error      string
	Notice     string
	Data       any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name, title string, data any, errMsg string) {
	listing := s.ctrl.Listing()
	p := page{
		Base:       base(r),
		Title:      title,
		Tab:        name,
		Channel:    listing.Channel,
		Authorized: listing.Authorized,
		Error:      errMsg,
		Notice:     r.URL.Query().Get("notice"),
		Data:       data,
	}
	if p.Error == "" {
		p.Error = r.URL.Query().Get("error")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[name].ExecuteTemplate(w, "layout.html", p); err != nil {
		s.log.Warn("page_render_failed", "page", name, "error", err.Error())
	}
}

func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), requestTimeout)
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if s.auth.Configured() && s.auth.Authorized() {
		s.redirect(w, r, "/broadcasts")
		return
	}
	s.redirect(w, r, "/connection")
}
