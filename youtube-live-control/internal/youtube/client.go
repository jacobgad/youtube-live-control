package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"time"
)

const (
	apiBase    = "https://www.googleapis.com/youtube/v3"
	uploadBase = "https://www.googleapis.com/upload/youtube/v3"

	// MaxThumbnailBytes is YouTube's upload limit.
	MaxThumbnailBytes = 2 << 20
)

// PrivacyOptions are the privacy statuses a broadcast can have.
var PrivacyOptions = []string{"public", "unlisted", "private"}

// ValidPrivacy reports whether p is one of PrivacyOptions.
func ValidPrivacy(p string) bool {
	for _, o := range PrivacyOptions {
		if o == p {
			return true
		}
	}
	return false
}

// Lifecycle, stream status and transition values as the API spells them.
const (
	LifeCreated      = "created"
	LifeReady        = "ready"
	LifeTestStarting = "testStarting"
	LifeTesting      = "testing"
	LifeLiveStarting = "liveStarting"
	LifeLive         = "live"
	LifeComplete     = "complete"

	StreamActive = "active"

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
	IsDefault       bool
	ThumbnailURL    string
	parts           rawParts
}

// Stream is a liveStream resource: the encoder's stream key and its ingestion settings.
type Stream struct {
	ID         string
	Title      string
	StreamKey  string
	Resolution string
	FrameRate  string
	IsReusable bool
	Status     string
}

// An update PUT overwrites every mutable field of each part sent; echoing the fetched
// parts is what keeps DVR, latency, embed and caption settings intact.
type rawParts struct {
	Snippet        map[string]any
	Status         map[string]any
	ContentDetails map[string]any
}

// StreamStatus is a liveStream's ingestion state.
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

// Client calls the YouTube Data API v3.
type Client struct {
	auth *Auth
	hc   *http.Client
}

// NewClient returns a Client authenticating with auth.
func NewClient(auth *Auth) *Client {
	return &Client{auth: auth, hc: &http.Client{Timeout: 30 * time.Second}}
}

type apiBroadcastItem struct {
	ID             string          `json:"id"`
	Snippet        json.RawMessage `json:"snippet"`
	Status         json.RawMessage `json:"status"`
	ContentDetails json.RawMessage `json:"contentDetails"`
}

func (i apiBroadcastItem) broadcast() Broadcast {
	parts := rawParts{Snippet: decodeMap(i.Snippet), Status: decodeMap(i.Status), ContentDetails: decodeMap(i.ContentDetails)}
	b := Broadcast{
		ID:              i.ID,
		Title:           stringField(parts.Snippet, "title"),
		Description:     stringField(parts.Snippet, "description"),
		PrivacyStatus:   stringField(parts.Status, "privacyStatus"),
		LifeCycleStatus: stringField(parts.Status, "lifeCycleStatus"),
		BoundStreamID:   stringField(parts.ContentDetails, "boundStreamId"),
		ThumbnailURL:    thumbnailURL(parts.Snippet),
		parts:           parts,
	}
	b.ScheduledStart, _ = time.Parse(time.RFC3339, stringField(parts.Snippet, "scheduledStartTime"))
	b.IsDefault, _ = parts.Snippet["isDefaultBroadcast"].(bool)
	if monitor, ok := parts.ContentDetails["monitorStream"].(map[string]any); ok {
		b.MonitorEnabled, _ = monitor["enableMonitorStream"].(bool)
	}
	return b
}

func thumbnailURL(snippet map[string]any) string {
	thumbs, ok := snippet["thumbnails"].(map[string]any)
	if !ok {
		return ""
	}
	for _, size := range []string{"maxres", "standard", "high", "medium", "default"} {
		if t, ok := thumbs[size].(map[string]any); ok {
			if u := stringField(t, "url"); u != "" {
				return u
			}
		}
	}
	return ""
}

func decodeMap(raw json.RawMessage) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return m
}

func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

type broadcastListResponse struct {
	Items []apiBroadcastItem `json:"items"`
}

const broadcastParts = "id,snippet,status,contentDetails"

// ListBroadcasts lists event broadcasts by "upcoming" or "active". Costs 1 quota unit.
func (c *Client) ListBroadcasts(ctx context.Context, broadcastStatus string) ([]Broadcast, error) {
	var out broadcastListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveBroadcasts", url.Values{
		"part":            {broadcastParts},
		"broadcastStatus": {broadcastStatus},
		"broadcastType":   {"event"},
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

// GetBroadcast fetches one broadcast; ok is false when it no longer exists. Costs 1 quota unit.
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

// No omitempty on the auto flags: every insert must state false explicitly, so only
// the buttons ever transition a broadcast.
type insertBody struct {
	Snippet struct {
		Title              string `json:"title"`
		Description        string `json:"description,omitempty"`
		ScheduledStartTime string `json:"scheduledStartTime"`
	} `json:"snippet"`
	Status struct {
		PrivacyStatus           string `json:"privacyStatus"`
		SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids"`
	} `json:"status"`
	ContentDetails struct {
		EnableAutoStart bool `json:"enableAutoStart"`
		EnableAutoStop  bool `json:"enableAutoStop"`
		MonitorStream   struct {
			EnableMonitorStream bool `json:"enableMonitorStream"`
		} `json:"monitorStream"`
	} `json:"contentDetails"`
}

// NewBroadcast is the input to InsertBroadcast.
type NewBroadcast struct {
	Title       string
	Description string
	Start       time.Time
	Privacy     string
}

func newInsertBody(n NewBroadcast) insertBody {
	var body insertBody
	body.Snippet.Title = n.Title
	body.Snippet.Description = n.Description
	body.Snippet.ScheduledStartTime = n.Start.UTC().Format(time.RFC3339)
	body.Status.PrivacyStatus = n.Privacy
	return body
}

// Documented read-only fields, stripped so the echoed body is valid input.
var readOnlyFields = map[string][]string{
	"snippet":        {"publishedAt", "channelId", "thumbnails", "isDefaultBroadcast", "liveChatId", "actualStartTime", "actualEndTime"},
	"status":         {"lifeCycleStatus", "recordingStatus", "madeForKids"},
	"contentDetails": {"boundStreamId", "boundStreamLastUpdateTimeMs"},
}

func updateBody(b Broadcast) map[string]any {
	snippet := cloneOrEmpty(b.parts.Snippet)
	snippet["title"] = b.Title
	snippet["description"] = b.Description
	snippet["scheduledStartTime"] = b.ScheduledStart.UTC().Format(time.RFC3339)
	status := cloneOrEmpty(b.parts.Status)
	status["privacyStatus"] = b.PrivacyStatus
	content := cloneOrEmpty(b.parts.ContentDetails)
	content["enableAutoStart"] = false
	content["enableAutoStop"] = false
	body := map[string]any{"id": b.ID, "snippet": snippet, "status": status, "contentDetails": content}
	for part, keys := range readOnlyFields {
		for _, key := range keys {
			delete(body[part].(map[string]any), key)
		}
	}
	return body
}

func cloneOrEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return maps.Clone(m)
}

// InsertBroadcast creates a scheduled broadcast. Costs 50 quota units.
func (c *Client) InsertBroadcast(ctx context.Context, n NewBroadcast) (Broadcast, error) {
	var out apiBroadcastItem
	err := c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts", url.Values{"part": {broadcastParts}}, newInsertBody(n), &out)
	if err != nil {
		return Broadcast{}, err
	}
	return out.broadcast(), nil
}

// UpdateBroadcast writes b over the broadcast as last fetched. Costs 50 quota units.
func (c *Client) UpdateBroadcast(ctx context.Context, b Broadcast) (Broadcast, error) {
	var out apiBroadcastItem
	err := c.do(ctx, http.MethodPut, apiBase+"/liveBroadcasts", url.Values{"part": {broadcastParts}}, updateBody(b), &out)
	if err != nil {
		return Broadcast{}, err
	}
	return out.broadcast(), nil
}

// DeleteBroadcast removes a broadcast (and its video). Costs 50 quota units.
func (c *Client) DeleteBroadcast(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, apiBase+"/liveBroadcasts", url.Values{"id": {id}}, nil, nil)
}

// Transition moves a broadcast to testing, live or complete. Costs 50 quota units.
func (c *Client) Transition(ctx context.Context, id, broadcastStatus string) error {
	return c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts/transition", url.Values{
		"part":            {"id,status"},
		"id":              {id},
		"broadcastStatus": {broadcastStatus},
	}, nil, nil)
}

// Bind attaches a liveStream to a broadcast. Costs 50 quota units.
func (c *Client) Bind(ctx context.Context, broadcastID, streamID string) error {
	return c.do(ctx, http.MethodPost, apiBase+"/liveBroadcasts/bind", url.Values{
		"part":     {"id,contentDetails"},
		"id":       {broadcastID},
		"streamId": {streamID},
	}, nil, nil)
}

type streamItem struct {
	ID      string `json:"id"`
	Snippet struct {
		Title string `json:"title"`
	} `json:"snippet"`
	CDN struct {
		Resolution    string `json:"resolution"`
		FrameRate     string `json:"frameRate"`
		IngestionInfo struct {
			StreamName string `json:"streamName"`
		} `json:"ingestionInfo"`
	} `json:"cdn"`
	Status struct {
		StreamStatus string `json:"streamStatus"`
		HealthStatus struct {
			Status string `json:"status"`
		} `json:"healthStatus"`
	} `json:"status"`
	ContentDetails struct {
		IsReusable bool `json:"isReusable"`
	} `json:"contentDetails"`
}

type streamListResponse struct {
	Items []streamItem `json:"items"`
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

// ListStreams lists the channel's stream keys. Costs 1 quota unit.
func (c *Client) ListStreams(ctx context.Context) ([]Stream, error) {
	var out streamListResponse
	err := c.do(ctx, http.MethodGet, apiBase+"/liveStreams", url.Values{
		"part":       {"id,snippet,cdn,status,contentDetails"},
		"mine":       {"true"},
		"maxResults": {"50"},
	}, nil, &out)
	if err != nil {
		return nil, err
	}
	streams := make([]Stream, 0, len(out.Items))
	for _, item := range out.Items {
		streams = append(streams, Stream{
			ID:         item.ID,
			Title:      item.Snippet.Title,
			StreamKey:  item.CDN.IngestionInfo.StreamName,
			Resolution: item.CDN.Resolution,
			FrameRate:  item.CDN.FrameRate,
			IsReusable: item.ContentDetails.IsReusable,
			Status:     item.Status.StreamStatus,
		})
	}
	return streams, nil
}

// Channel identifies the YouTube channel the stored token acts on.
type Channel struct {
	ID      string
	Title   string
	Country string
}

// Category is a YouTube video category available in the channel's country.
type Category struct {
	ID    string
	Title string
}

// MyChannel returns the channel the token acts on. Costs 1 quota unit.
func (c *Client) MyChannel(ctx context.Context) (Channel, error) {
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title   string `json:"title"`
				Country string `json:"country"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, apiBase+"/channels", url.Values{"part": {"snippet"}, "mine": {"true"}}, nil, &out)
	if err != nil {
		return Channel{}, err
	}
	if len(out.Items) == 0 {
		return Channel{}, errors.New("token is not associated with any YouTube channel")
	}
	return Channel{ID: out.Items[0].ID, Title: out.Items[0].Snippet.Title, Country: out.Items[0].Snippet.Country}, nil
}

// Category ids are global; only the assignable set varies by region, so a channel
// without a country gets the US list rather than none.
const fallbackRegion = "US"

// ListCategories lists the assignable video categories for a country. Costs 1 quota unit.
func (c *Client) ListCategories(ctx context.Context, regionCode string) ([]Category, error) {
	if regionCode == "" {
		regionCode = fallbackRegion
	}
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title      string `json:"title"`
				Assignable bool   `json:"assignable"`
			} `json:"snippet"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, apiBase+"/videoCategories", url.Values{"part": {"snippet"}, "regionCode": {regionCode}}, nil, &out)
	if err != nil {
		return nil, err
	}
	categories := make([]Category, 0, len(out.Items))
	for _, item := range out.Items {
		if item.Snippet.Assignable {
			categories = append(categories, Category{ID: item.ID, Title: item.Snippet.Title})
		}
	}
	return categories, nil
}

func (c *Client) videoSnippet(ctx context.Context, videoID string) (map[string]any, bool, error) {
	var out struct {
		Items []struct {
			Snippet map[string]any `json:"snippet"`
		} `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, apiBase+"/videos", url.Values{"part": {"snippet"}, "id": {videoID}}, nil, &out)
	if err != nil {
		return nil, false, err
	}
	if len(out.Items) == 0 {
		return nil, false, nil
	}
	return out.Items[0].Snippet, true, nil
}

// VideoCategory reads the category of the broadcast's video; ok is false when the
// video does not exist. Costs 1 quota unit.
func (c *Client) VideoCategory(ctx context.Context, videoID string) (string, bool, error) {
	snippet, ok, err := c.videoSnippet(ctx, videoID)
	if err != nil || !ok {
		return "", ok, err
	}
	return stringField(snippet, "categoryId"), true, nil
}

// Read-only video snippet fields, stripped so the echoed body is valid input.
var videoSnippetReadOnly = []string{"publishedAt", "channelId", "channelTitle", "thumbnails", "liveBroadcastContent", "localized"}

func videoUpdateBody(videoID string, snippet map[string]any, categoryID string) map[string]any {
	snippet = cloneOrEmpty(snippet)
	snippet["categoryId"] = categoryID
	for _, key := range videoSnippetReadOnly {
		delete(snippet, key)
	}
	return map[string]any{"id": videoID, "snippet": snippet}
}

// SetVideoCategory writes the video's category over its snippet as fetched (the PUT
// replaces the whole part); an unchanged category is a no-op that costs only the read.
// Costs 1 quota unit, plus 50 when a write is needed.
func (c *Client) SetVideoCategory(ctx context.Context, videoID, categoryID string) error {
	snippet, ok, err := c.videoSnippet(ctx, videoID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("video not found")
	}
	if stringField(snippet, "categoryId") == categoryID {
		return nil
	}
	return c.do(ctx, http.MethodPut, apiBase+"/videos", url.Values{"part": {"snippet"}}, videoUpdateBody(videoID, snippet, categoryID), nil)
}

// SetThumbnail uploads a thumbnail for the broadcast. Costs 50 quota units.
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
