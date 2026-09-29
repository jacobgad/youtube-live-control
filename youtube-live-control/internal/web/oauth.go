package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const exchangeTimeout = 30 * time.Second

type connectionData struct {
	Configured  bool
	External    bool
	RedirectURI string
	AuthURL     string
}

func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	s.renderConnection(w, r, "")
}

func (s *Server) renderConnection(w http.ResponseWriter, r *http.Request, errMsg string) {
	data := connectionData{Configured: s.auth.Configured()}
	if data.Configured {
		data.RedirectURI = s.redirectURI()
		data.External = s.opts.ExternalURL != ""
		state, err := s.newState(data.RedirectURI)
		if err != nil {
			http.Error(w, "failed to create OAuth state", http.StatusInternalServerError)
			return
		}
		data.AuthURL = s.auth.AuthURL(data.RedirectURI, state)
	}
	s.render(w, r, "connection", "Connection", data, errMsg)
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

// exchangeContext outlives the browser request: a single-use code must not be lost
// because the volunteer closed the tab mid-exchange.
func exchangeContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), exchangeTimeout)
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
		s.renderConnection(w, r, "Paste the full URL from the browser's address bar after consenting.")
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
			s.renderConnection(w, r, "That sign-in attempt has expired. Use the Connect link again, then paste the new URL.")
			return
		}
		redirectURI = entry.redirectURI
	}
	ctx, cancel := exchangeContext(r)
	defer cancel()
	if err := s.auth.Exchange(ctx, code, redirectURI); err != nil {
		s.log.Error("oauth_exchange_failed", "error", err.Error())
		s.renderConnection(w, r, "Token exchange failed: "+err.Error())
		return
	}
	s.redirect(w, r, "/broadcasts", "notice", "Connected. The entities in Home Assistant are now live.")
}

func writeResult(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = resultPage.Execute(w, text)
}

var resultPage = template.Must(template.New("result").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>YouTube Live Control</title>
<style>body { font-family: -apple-system, system-ui, sans-serif; margin: 4rem auto; max-width: 40rem; padding: 0 1rem; }</style>
</head><body><h1>YouTube Live Control</h1><p>{{.}}</p></body></html>
`))
