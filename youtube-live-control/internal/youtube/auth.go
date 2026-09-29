// Package youtube is the Google side of the add-on: OAuth 2.0 with the refresh token
// persisted under /data, and a minimal YouTube Data API v3 client for live broadcasts.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/atomicfile"
)

const (
	authEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint = "https://oauth2.googleapis.com/token" //nolint:gosec // OAuth endpoint URL, not a credential

	// Scope is the only scope requested: full manage access, required by
	// liveBroadcasts.insert/update/transition and thumbnails.set.
	Scope = "https://www.googleapis.com/auth/youtube.force-ssl"

	tokenExpirySlack = time.Minute
)

// ErrNotAuthorized is returned while no usable refresh token is stored.
var ErrNotAuthorized = errors.New("youtube: not authorized; open the add-on web UI and connect a Google account")

// Auth holds the OAuth client and the stored refresh token, and mints access tokens.
type Auth struct {
	clientID     string
	clientSecret string
	tokenPath    string
	hc           *http.Client
	log          *slog.Logger
	now          func() time.Time

	mu       sync.Mutex
	refresh  string
	access   string
	expiry   time.Time
	onChange func(authorized bool)
}

// NewAuth wires the OAuth client; call Load before use.
func NewAuth(clientID, clientSecret, tokenPath string, log *slog.Logger) *Auth {
	if log == nil {
		log = slog.Default()
	}
	return &Auth{
		clientID:     clientID,
		clientSecret: clientSecret,
		tokenPath:    tokenPath,
		hc:           &http.Client{Timeout: 30 * time.Second},
		log:          log,
		now:          time.Now,
	}
}

// String renders the client without its secret or tokens, so formatting an Auth
// (or anything holding one) can never leak a credential into logs.
func (a *Auth) String() string {
	return fmt.Sprintf("youtube.Auth{clientID=%s authorized=%v}", a.clientID, a.Authorized())
}

// GoString mirrors String for %#v, which bypasses Stringer.
func (a *Auth) GoString() string { return a.String() }

// LogValue renders the client for slog without the secret or tokens.
func (a *Auth) LogValue() slog.Value {
	return slog.GroupValue(slog.String("clientID", a.clientID), slog.Bool("authorized", a.Authorized()))
}

// OnChange registers the single observer notified when authorization is gained or lost.
func (a *Auth) OnChange(fn func(authorized bool)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.onChange = fn
}

// Configured reports whether a Google OAuth client is set in the add-on options.
func (a *Auth) Configured() bool { return a.clientID != "" && a.clientSecret != "" }

type tokenFile struct {
	RefreshToken string `json:"refresh_token"`
}

// Load reads the persisted refresh token; a missing file is a normal first run.
func (a *Auth) Load() error {
	data, err := os.ReadFile(a.tokenPath) //nolint:gosec // path is fixed by the add-on
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", a.tokenPath, err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return fmt.Errorf("parse %s: %w", a.tokenPath, err)
	}
	a.mu.Lock()
	a.refresh = tf.RefreshToken
	a.mu.Unlock()
	if tf.RefreshToken != "" {
		a.log.Info("token_loaded", "path", a.tokenPath)
	}
	return nil
}

// Authorized reports whether a refresh token is stored.
func (a *Auth) Authorized() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refresh != ""
}

// AuthURL builds the Google consent URL. prompt=consent guarantees a refresh token on
// every completed consent; select_account forces the chooser so an account managing
// several channels (Brand Accounts) always picks which channel the token acts on.
func (a *Auth) AuthURL(redirectURI, state string) string {
	q := url.Values{
		"client_id":     {a.clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {Scope},
		"access_type":   {"offline"},
		"prompt":        {"select_account consent"},
		"state":         {state},
	}
	return authEndpoint + "?" + q.Encode()
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Exchange trades an authorization code for tokens and persists the refresh token.
func (a *Auth) Exchange(ctx context.Context, code, redirectURI string) error {
	resp, err := a.tokenRequest(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
	})
	if err != nil {
		return err
	}
	if resp.RefreshToken == "" {
		return errors.New("google returned no refresh token; remove the app's access at myaccount.google.com/permissions and connect again")
	}
	if err := a.persist(resp.RefreshToken); err != nil {
		return err
	}
	a.mu.Lock()
	a.refresh = resp.RefreshToken
	a.access = resp.AccessToken
	a.expiry = a.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	notify := a.onChange
	a.mu.Unlock()
	a.log.Info("authorization_granted", "path", a.tokenPath)
	if notify != nil {
		notify(true)
	}
	return nil
}

// AccessToken returns a valid bearer token, refreshing when the cached one is stale.
func (a *Auth) AccessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	refresh := a.refresh
	if refresh == "" {
		a.mu.Unlock()
		return "", ErrNotAuthorized
	}
	if a.access != "" && a.now().Before(a.expiry.Add(-tokenExpirySlack)) {
		token := a.access
		a.mu.Unlock()
		return token, nil
	}
	a.mu.Unlock()

	resp, err := a.tokenRequest(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh},
	})
	if err != nil {
		var oe *oauthError
		if errors.As(err, &oe) && oe.Code == "invalid_grant" {
			a.revoke(refresh)
			return "", ErrNotAuthorized
		}
		return "", err
	}
	a.mu.Lock()
	a.access = resp.AccessToken
	a.expiry = a.now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	a.mu.Unlock()
	return resp.AccessToken, nil
}

// Flips the UI and Authorization sensor to unauthorized instead of erroring forever;
// only the token that failed is cleared, so a consent that landed meanwhile is kept.
func (a *Auth) revoke(failed string) {
	a.mu.Lock()
	if a.refresh != failed {
		a.mu.Unlock()
		return
	}
	a.refresh = ""
	a.access = ""
	notify := a.onChange
	a.mu.Unlock()
	if err := os.Remove(a.tokenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		a.log.Warn("token_delete_failed", "path", a.tokenPath, "error", err.Error())
	}
	a.log.Warn("authorization_revoked", "detail", "google rejected the refresh token; reconnect via the web UI")
	if notify != nil {
		notify(false)
	}
}

type oauthError struct {
	Code        string
	Description string
	HTTPStatus  int
}

func (e *oauthError) Error() string {
	return fmt.Sprintf("oauth token request failed: %s (%s, HTTP %d)", e.Code, e.Description, e.HTTPStatus)
}

func (a *Auth) tokenRequest(ctx context.Context, form url.Values) (tokenResponse, error) {
	form.Set("client_id", a.clientID)
	form.Set("client_secret", a.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.hc.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return tokenResponse{}, fmt.Errorf("token response is not JSON (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK || tr.Error != "" {
		return tokenResponse{}, &oauthError{Code: tr.Error, Description: tr.ErrorDescription, HTTPStatus: resp.StatusCode}
	}
	return tr, nil
}

func (a *Auth) persist(refreshToken string) error {
	data, err := json.Marshal(tokenFile{RefreshToken: refreshToken}) //nolint:gosec // persisting the refresh token to /data is this function's purpose
	if err != nil {
		return err
	}
	return atomicfile.Write(a.tokenPath, data, 0o600)
}
