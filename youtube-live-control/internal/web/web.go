// Package web serves the ingress console for one-time Google consent and the
// plain-port OAuth callback that Google can actually redirect to.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// Both ports must match config.yaml (ingress_port and ports); the callback port is
// exposed on the host because Google must be able to redirect the browser to it.
const (
	ingressPort  = 8099
	callbackPort = 8098

	stateTTL        = 15 * time.Minute
	shutdownTimeout = 5 * time.Second
	exchangeTimeout = 30 * time.Second
)

// Server is the ingress UI plus the OAuth callback listener.
type Server struct {
	auth *youtube.Auth
	opts config.Options
	log  *slog.Logger

	mu     sync.Mutex
	states map[string]stateEntry
}

type stateEntry struct {
	redirectURI string
	expires     time.Time
}

// New builds the server; Run starts it.
func New(auth *youtube.Auth, opts config.Options, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{auth: auth, opts: opts, log: log, states: map[string]stateEntry{}}
}

// Run serves both listeners until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ingressMux := http.NewServeMux()
	ingressMux.HandleFunc("GET /{$}", s.handleHome)
	ingressMux.HandleFunc("POST /manual", s.handleManual)

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

// exchangeContext outlives the browser request: a single-use code must not be lost
// because the volunteer closed the tab mid-exchange.
func exchangeContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), exchangeTimeout)
}

func serve(srv *http.Server, name string, log *slog.Logger) error {
	log.Info("http_listening", "server", name, "addr", srv.Addr)
	err := srv.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return fmt.Errorf("%s server: %w", name, err)
}

type pageData struct {
	Configured  bool
	Authorized  bool
	External    bool
	RedirectURI string
	AuthURL     string
	Error       string
	Notice      string
}

func (s *Server) handleHome(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "", "")
}

func (s *Server) renderPage(w http.ResponseWriter, errMsg, notice string) {
	data := pageData{
		Configured: s.auth.Configured(),
		Authorized: s.auth.Authorized(),
		Error:      errMsg,
		Notice:     notice,
	}
	if data.Configured {
		data.RedirectURI = s.redirectURI()
		data.External = s.usesExternalURL()
		state, err := s.newState(data.RedirectURI)
		if err != nil {
			http.Error(w, "failed to create OAuth state", http.StatusInternalServerError)
			return
		}
		data.AuthURL = s.auth.AuthURL(data.RedirectURI, state)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, data); err != nil {
		s.log.Warn("page_render_failed", "error", err.Error())
	}
}

// Google's OAuth policy rejects plain-http redirects except to loopback, so the
// default is localhost (the browser then shows the code in a failed tab, which the
// paste form accepts); external_url is for an https reverse proxy in front of :8098.
func (s *Server) redirectURI() string {
	if s.opts.ExternalURL != "" {
		return s.opts.ExternalURL + "/oauth/callback"
	}
	return fmt.Sprintf("http://localhost:%d/oauth/callback", callbackPort)
}

func (s *Server) usesExternalURL() bool { return s.opts.ExternalURL != "" }

func (s *Server) newState(redirectURI string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	state := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.states {
		if now.After(v.expires) {
			delete(s.states, k)
		}
	}
	s.states[state] = stateEntry{redirectURI: redirectURI, expires: now.Add(stateTTL)}
	return state, nil
}

func (s *Server) takeState(state string) (stateEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.states[state]
	if !ok || time.Now().After(entry.expires) {
		return stateEntry{}, false
	}
	delete(s.states, state)
	return entry, true
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		s.log.Warn("oauth_consent_denied", "error", errCode)
		writeResult(w, http.StatusBadRequest, "Google reported: "+errCode)
		return
	}
	entry, ok := s.takeState(q.Get("state"))
	if !ok {
		s.log.Warn("oauth_state_rejected")
		writeResult(w, http.StatusBadRequest, "Unknown or expired sign-in attempt. Reopen the add-on page and try again.")
		return
	}
	ctx, cancel := exchangeContext(r)
	defer cancel()
	if err := s.auth.Exchange(ctx, q.Get("code"), entry.redirectURI); err != nil {
		s.log.Error("oauth_exchange_failed", "error", err.Error())
		writeResult(w, http.StatusBadGateway, "Token exchange failed: "+err.Error())
		return
	}
	writeResult(w, http.StatusOK, "Connected. You can close this tab; the entities in Home Assistant are now live.")
}

// Fallback for networks where the callback port is unreachable from the browser.
func (s *Server) handleManual(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("response"))
	if raw == "" {
		s.renderPage(w, "Paste the full URL from the browser's address bar after consenting.", "")
		return
	}
	code, state := raw, ""
	if u, err := url.Parse(raw); err == nil && u.Query().Get("code") != "" {
		code = u.Query().Get("code")
		state = u.Query().Get("state")
	}
	redirectURI := s.redirectURI()
	if state != "" {
		entry, ok := s.takeState(state)
		if !ok {
			s.renderPage(w, "That sign-in attempt has expired. Use the Connect link again, then paste the new URL.", "")
			return
		}
		redirectURI = entry.redirectURI
	}
	ctx, cancel := exchangeContext(r)
	defer cancel()
	if err := s.auth.Exchange(ctx, code, redirectURI); err != nil {
		s.log.Error("oauth_exchange_failed", "error", err.Error())
		s.renderPage(w, "Token exchange failed: "+err.Error(), "")
		return
	}
	s.renderPage(w, "", "Connected. The entities in Home Assistant are now live.")
}

func writeResult(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = resultPage.Execute(w, text)
}

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>YouTube Live Control</title>
<style>
body { font-family: -apple-system, system-ui, sans-serif; margin: 2rem auto; max-width: 44rem; padding: 0 1rem; color: #1c1c1e; }
h1 { font-size: 1.4rem; } h2 { font-size: 1.05rem; margin-top: 2rem; }
code { background: #f2f2f7; padding: 0.15rem 0.4rem; border-radius: 4px; word-break: break-all; }
.badge { display: inline-block; padding: 0.25rem 0.7rem; border-radius: 999px; font-weight: 600; }
.ok { background: #d1f2d9; color: #1d7a36; } .warn { background: #fde3c9; color: #ad5700; }
.error { background: #fdd8d6; color: #b3261e; padding: 0.7rem 1rem; border-radius: 8px; }
.notice { background: #d1f2d9; color: #1d7a36; padding: 0.7rem 1rem; border-radius: 8px; }
a.button { display: inline-block; background: #c00; color: #fff; padding: 0.6rem 1.2rem; border-radius: 8px; text-decoration: none; font-weight: 600; }
input[type=text] { width: 100%; padding: 0.5rem; border: 1px solid #c7c7cc; border-radius: 6px; box-sizing: border-box; }
button { margin-top: 0.5rem; padding: 0.5rem 1rem; border: 0; border-radius: 6px; background: #1c1c1e; color: #fff; font-weight: 600; }
ol li { margin-bottom: 0.4rem; }
</style>
</head>
<body>
<h1>YouTube Live Control</h1>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .Notice}}<p class="notice">{{.Notice}}</p>{{end}}
{{if not .Configured}}
<p><span class="badge warn">Not configured</span></p>
<p>Set <code>google_client_id</code> and <code>google_client_secret</code> on the add-on's
<strong>Configuration</strong> tab, then restart the add-on.</p>
<ol>
<li>In <a href="https://console.cloud.google.com/apis/credentials" target="_blank" rel="noreferrer">Google Cloud Console</a>, create a project and enable the <strong>YouTube Data API v3</strong>.</li>
<li>Configure the OAuth consent screen (type <em>External</em> is fine; add the channel's Google account as a test user).</li>
<li>Create an <strong>OAuth client ID</strong> of type <strong>Desktop app</strong>. No redirect URI needs to be registered for this type.</li>
</ol>
{{else}}
{{if .Authorized}}
<p><span class="badge ok">Connected</span> — the add-on can manage the channel's live broadcasts.</p>
<p>To switch accounts or repair access, connect again below.</p>
{{else}}
<p><span class="badge warn">Not connected</span> — consent once and the refresh token is kept in <code>/data</code>.</p>
{{end}}
{{if .External}}
<h2>1 · Register the redirect URI</h2>
<p>Your OAuth client must be of type <strong>Web application</strong> with this exact <strong>authorized redirect URI</strong>
(Google requires https for anything other than localhost):</p>
<p><code>{{.RedirectURI}}</code></p>
<h2>2 · Consent</h2>
<p><a class="button" href="{{.AuthURL}}" target="_blank" rel="noreferrer">Connect Google account</a></p>
<p>Sign in with the channel's account and allow <em>Manage your YouTube account</em>. Google redirects back
through your proxy to the add-on and the connection completes on its own.</p>
<h2>If the redirect page fails to load</h2>
<p>The consent still succeeded — the code is in the address bar. Paste the full URL here:</p>
{{else}}
<h2>1 · Consent</h2>
<p>Use an OAuth client of type <strong>Desktop app</strong> — Google allows its <code>localhost</code> redirect
without registration. (Redirect URI in use: <code>{{.RedirectURI}}</code>.)</p>
<p><a class="button" href="{{.AuthURL}}" target="_blank" rel="noreferrer">Connect Google account</a></p>
<p>Sign in with the channel's account and allow <em>Manage your YouTube account</em>.</p>
<h2>2 · Paste the result</h2>
<p>After consenting, Google sends the browser to <code>localhost</code>, which shows a "can't connect" page
unless this browser is running on the Home Assistant machine. <strong>That is expected</strong> — the
consent succeeded and the code is in that tab's address bar. Copy the whole URL and paste it here:</p>
{{end}}
<form method="post" action="manual">
<input type="text" name="response" placeholder="http://localhost:8098/oauth/callback?state=…&code=…" autocomplete="off">
<button type="submit">Finish connection</button>
</form>
<p>Have a public https hostname that forwards to this add-on's port 8098? Set the <code>external_url</code>
option and the redirect completes automatically instead.</p>
{{end}}
</body>
</html>
`))

var resultPage = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>YouTube Live Control</title>
<style>body { font-family: -apple-system, system-ui, sans-serif; margin: 4rem auto; max-width: 40rem; padding: 0 1rem; }</style>
</head><body><h1>YouTube Live Control</h1><p>{{.}}</p></body></html>
`))
