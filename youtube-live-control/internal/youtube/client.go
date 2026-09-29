package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	apiBase    = "https://www.googleapis.com/youtube/v3"
	uploadBase = "https://www.googleapis.com/upload/youtube/v3"

	// MaxThumbnailBytes is YouTube's thumbnail upload limit (2 MB).
	MaxThumbnailBytes = 2 << 20
)

// Broadcast lifecycle and stream status values as reported by the API.
const (
	LifeCreated      = "created"
	LifeReady        = "ready"
	LifeTestStarting = "testStarting"
	LifeTesting      = "testing"
	LifeLiveStarting = "liveStarting"
	LifeLive         = "live"
	LifeComplete     = "complete"
	LifeRevoked      = "revoked"

	StreamActive = "active"
	HealthNoData = "noData"

	TransitionTesting  = "testing"
	TransitionLive     = "live"
	TransitionComplete = "complete"
)

// Broadcast is the add-on's view of a liveBroadcast resource.
type Broadcast struct {
	ID              string
	Title           string
	Description     string
	ScheduledStart  time.Time
	PrivacyStatus   string
	LifeCycleStatus string
	BoundStreamID   string
	MonitorEnabled  bool
}

// StreamStatus is the bound liveStream's ingestion state.
type StreamStatus struct {
	Status string // created, ready, active, inactive, error
	Health string // good, ok, bad, noData; only meaningful while active
}

// APIError is a decoded YouTube API error.
type APIError struct {
	StatusCode int
	Reason     string
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("youtube api: HTTP %d %s: %s", e.StatusCode, e.Reason, e.Message)
}

// HasReason reports whether err is an APIError with the given reason.
func HasReason(err error, reason string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Reason == reason
}

// Client calls the YouTube Data API v3 with tokens minted by Auth.
type Client struct {
	auth *Auth
	hc   *http.Client
	log  *slog.Logger
}

// NewClient wires the API client to its token source.
func NewClient(auth *Auth, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{auth: auth, hc: &http.Client{Timeout: 30 * time.Second}, log: log}
}

type apiBroadcastItem struct {
	ID      string `json:"id"`
	Snippet struct {
		Title              string `json:"title"`
		Description        string `json:"description"`
		ScheduledStartTime string `json:"scheduledStartTime"`
	} `json:"snippet"`
	Status struct {
		LifeCycleStatus string `json:"lifeCycleStatus"`
		PrivacyStatus   string `json:"privacyStatus"`
	} `json:"status"`
	ContentDetails struct {
		BoundStreamID string `json:"boundStreamId"`
		MonitorStream struct {
			EnableMonitorStream bool `json:"enableMonitorStream"`
		} `json:"monitorStream"`
	} `json:"contentDetails"`
}

func (i apiBroadcastItem) broadcast() Broadcast {
	start, _ := time.Parse(time.RFC3339, i.Snippet.ScheduledStartTime)
	return Broadcast{
		ID:              i.ID,
		Title:           i.Snippet.Title,
		Description:     i.Snippet.Description,
		ScheduledStart:  start,
		PrivacyStatus:   i.Status.PrivacyStatus,
		LifeCycleStatus: i.Status.LifeCycleStatus,
		BoundStreamID:   i.ContentDetails.BoundStreamID,
		MonitorEnabled:  i.ContentDetails.MonitorStream.EnableMonitorStream,
	}
}

type broadcastListResponse struct {
	Items []apiBroadcastItem `json:"items"`
}

const broadcastParts = "id,snippet,status,contentDetails"

// ListBroadcasts fetches the channel's broadcasts by lifecycle filter
// ("upcoming" or "active"). Costs 1 quota unit.
func (c *Client) ListBroadcasts(ctx context.Context, broadcastStatus string) ([]Broadcast, error) {
	var out broadcastListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveBroadcasts", url.Values{
		"part":            {broadcastParts},
		"broadcastStatus": {broadcastStatus},
		"maxResults":      {"50"},
	}, nil, &out)
	if err != nil {
		return nil, err
	}
	list := make([]Broadcast, 0, len(out.Items))
	for _, item := range out.Items {
		list = append(list, item.broadcast())
	}
	return list, nil
}

// GetBroadcast fetches one broadcast by id; ok is false when it no longer exists.
// Costs 1 quota unit.
func (c *Client) GetBroadcast(ctx context.Context, id string) (Broadcast, bool, error) {
	var out broadcastListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveBroadcasts", url.Values{
		"part": {broadcastParts},
		"id":   {id},
	}, nil, &out)
	if err != nil {
		return Broadcast{}, false, err
	}
	if len(out.Items) == 0 {
		return Broadcast{}, false, nil
	}
	return out.Items[0].broadcast(), true, nil
}

type writeBroadcast struct {
	ID      string `json:"id,omitempty"`
	Snippet struct {
		Title              string `json:"title"`
		Description        string `json:"description,omitempty"`
		ScheduledStartTime string `json:"scheduledStartTime"`
	} `json:"snippet"`
	Status struct {
		PrivacyStatus           string `json:"privacyStatus"`
		SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
	} `json:"status"`
	// enableAutoStart and enableAutoStop are always serialized (no omitempty) so
	// every insert and update states false explicitly: transitions happen only
	// through the Go Live and End Stream buttons, never because OBS started or stopped.
	ContentDetails struct {
		EnableAutoStart bool `json:"enableAutoStart"`
		EnableAutoStop  bool `json:"enableAutoStop"`
		MonitorStream   struct {
			EnableMonitorStream bool `json:"enableMonitorStream"`
		} `json:"monitorStream"`
	} `json:"contentDetails"`
}

func writeBody(b Broadcast) writeBroadcast {
	var w writeBroadcast
	w.ID = b.ID
	w.Snippet.Title = b.Title
	w.Snippet.Description = b.Description
	w.Snippet.ScheduledStartTime = b.ScheduledStart.UTC().Format(time.RFC3339)
	w.Status.PrivacyStatus = b.PrivacyStatus
	w.ContentDetails.EnableAutoStart = false
	w.ContentDetails.EnableAutoStop = false
	w.ContentDetails.MonitorStream.EnableMonitorStream = b.MonitorEnabled
	return w
}

// InsertBroadcast creates a scheduled broadcast with auto start/stop off and the
// monitor stream disabled, so ready → live is a single transition. Costs 50 quota units.
func (c *Client) InsertBroadcast(ctx context.Context, title string, start time.Time, privacy string) (Broadcast, error) {
	body := writeBody(Broadcast{Title: title, ScheduledStart: start, PrivacyStatus: privacy, MonitorEnabled: false})
	body.ID = ""
	var out apiBroadcastItem
	err := c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts", url.Values{"part": {broadcastParts}}, body, &out)
	if err != nil {
		return Broadcast{}, err
	}
	return out.broadcast(), nil
}

// UpdateBroadcast replaces the broadcast's snippet, status and contentDetails with b,
// keeping auto start/stop off. Costs 50 quota units.
func (c *Client) UpdateBroadcast(ctx context.Context, b Broadcast) (Broadcast, error) {
	var out apiBroadcastItem
	err := c.do(ctx, http.MethodPut, apiBase+"/liveBroadcasts", url.Values{"part": {broadcastParts}}, writeBody(b), &out)
	if err != nil {
		return Broadcast{}, err
	}
	return out.broadcast(), nil
}

// Transition moves a broadcast to testing, live or complete. Costs 50 quota units.
func (c *Client) Transition(ctx context.Context, id, broadcastStatus string) error {
	return c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts/transition", url.Values{
		"part":            {"id,status"},
		"id":              {id},
		"broadcastStatus": {broadcastStatus},
	}, nil, nil)
}

// Bind attaches a liveStream (the encoder's stream key) to a broadcast. Costs 50 quota units.
func (c *Client) Bind(ctx context.Context, broadcastID, streamID string) error {
	return c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts/bind", url.Values{
		"part":     {"id,contentDetails"},
		"id":       {broadcastID},
		"streamId": {streamID},
	}, nil, nil)
}

type streamListResponse struct {
	Items []struct {
		ID     string `json:"id"`
		Status struct {
			StreamStatus string `json:"streamStatus"`
			HealthStatus struct {
				Status string `json:"status"`
			} `json:"healthStatus"`
		} `json:"status"`
		ContentDetails struct {
			IsReusable bool `json:"isReusable"`
		} `json:"contentDetails"`
	} `json:"items"`
}

// StreamStatus fetches the ingestion status of one liveStream. Costs 1 quota unit.
func (c *Client) StreamStatus(ctx context.Context, streamID string) (StreamStatus, error) {
	var out streamListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveStreams", url.Values{
		"part": {"id,status"},
		"id":   {streamID},
	}, nil, &out)
	if err != nil {
		return StreamStatus{}, err
	}
	if len(out.Items) == 0 {
		return StreamStatus{}, nil
	}
	return StreamStatus{Status: out.Items[0].Status.StreamStatus, Health: out.Items[0].Status.HealthStatus.Status}, nil
}

// DefaultStreamID picks the channel's reusable liveStream (the persistent stream key
// OBS is configured with), falling back to the first stream. Costs 1 quota unit.
func (c *Client) DefaultStreamID(ctx context.Context) (string, error) {
	var out streamListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveStreams", url.Values{
		"part":       {"id,contentDetails"},
		"mine":       {"true"},
		"maxResults": {"50"},
	}, nil, &out)
	if err != nil {
		return "", err
	}
	for _, item := range out.Items {
		if item.ContentDetails.IsReusable {
			return item.ID, nil
		}
	}
	if len(out.Items) > 0 {
		return out.Items[0].ID, nil
	}
	return "", nil
}

// ConcurrentViewers reads the live viewer count off the video resource. Costs 1 quota unit.
func (c *Client) ConcurrentViewers(ctx context.Context, videoID string) (int, error) {
	var out struct {
		Items []struct {
			LiveStreamingDetails struct {
				ConcurrentViewers string `json:"concurrentViewers"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, apiBase+"/videos", url.Values{
		"part": {"liveStreamingDetails"},
		"id":   {videoID},
	}, nil, &out)
	if err != nil {
		return 0, err
	}
	if len(out.Items) == 0 {
		return 0, nil
	}
	viewers, _ := strconv.Atoi(out.Items[0].LiveStreamingDetails.ConcurrentViewers)
	return viewers, nil
}

// SetThumbnail uploads a thumbnail image for the broadcast's video. Costs 50 quota units.
func (c *Client) SetThumbnail(ctx context.Context, videoID, contentType string, image []byte) error {
	if len(image) > MaxThumbnailBytes {
		return fmt.Errorf("thumbnail is %d bytes; YouTube's limit is %d", len(image), MaxThumbnailBytes)
	}
	token, err := c.auth.AccessToken(ctx)
	if err != nil {
		return err
	}
	u := uploadBase + "/thumbnails/set?" + url.Values{"videoId": {videoID}, "uploadType": {"media"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(image))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("thumbnail upload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, endpoint string, query url.Values, body, out any) error {
	token, err := c.auth.AccessToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+"?"+query.Encode(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func decodeAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	apiErr := &APIError{StatusCode: resp.StatusCode}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		apiErr.Message = payload.Error.Message
		if len(payload.Error.Errors) > 0 {
			apiErr.Reason = payload.Error.Errors[0].Reason
		}
	}
	return apiErr
}
