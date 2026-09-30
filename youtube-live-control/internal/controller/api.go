package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jacobgad/youtube-live-control/internal/mqtt"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

// The web UI drives the same single-writer queue as the MQTT entities, so producer
// edits and volunteer commands never interleave and every write is read back.

// Listing is the cached view of the channel's broadcasts.
type Listing struct {
	Broadcasts []youtube.Broadcast
	Stale      []youtube.Broadcast
	SelectedID string
	Channel    string
	Authorized bool
}

// Listing returns the cache; it costs no quota.
func (c *Controller) Listing() Listing {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Listing{
		Broadcasts: append([]youtube.Broadcast(nil), c.session.broadcasts...),
		Stale:      append([]youtube.Broadcast(nil), c.session.stale...),
		SelectedID: c.session.selectedID,
		Channel:    c.session.channel,
		Authorized: c.session.authorized,
	}
}

// Broadcast reads the cache, refreshing once for something scheduled moments ago.
func (c *Controller) Broadcast(ctx context.Context, id string) (youtube.Broadcast, bool) {
	if b, ok := c.cached(id); ok {
		return b, true
	}
	c.refreshList(ctx)
	return c.cached(id)
}

func (c *Controller) cached(id string) (youtube.Broadcast, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, b := range c.session.broadcasts {
		if b.ID == id {
			return b, true
		}
	}
	for _, b := range c.session.stale {
		if b.ID == id {
			return b, true
		}
	}
	return youtube.Broadcast{}, false
}

// Streams lists the channel's stream keys. Costs 1 quota unit.
func (c *Controller) Streams(ctx context.Context) ([]youtube.Stream, error) {
	return c.yt.ListStreams(ctx)
}

// Categories lists the video categories Studio offers, sorted by title. Costs 1 quota unit.
func (c *Controller) Categories(ctx context.Context) ([]youtube.Category, error) {
	return c.yt.ListCategories(ctx)
}

// VideoCategory reads a broadcast's current category; ok is false when the video is gone. Costs 1 quota unit.
func (c *Controller) VideoCategory(ctx context.Context, id string) (string, bool, error) {
	return c.yt.VideoCategory(ctx, id)
}

// Edit is a change to a broadcast; an empty StreamID or CategoryID leaves that alone.
type Edit struct {
	Title       string
	Description string
	Start       time.Time
	Privacy     string
	StreamID    string
	CategoryID  string
}

// Validate reports the first problem as a user-facing message.
func (e Edit) Validate() error {
	title := strings.TrimSpace(e.Title)
	switch {
	case title == "":
		return errors.New("title is required")
	case utf8.RuneCountInString(title) > mqtt.MaxTitleLength:
		return fmt.Errorf("title is longer than %d characters", mqtt.MaxTitleLength)
	case utf8.RuneCountInString(e.Description) > 5000:
		return errors.New("description is longer than 5000 characters")
	case e.Start.IsZero():
		return errors.New("scheduled start is required")
	case !youtube.ValidPrivacy(e.Privacy):
		return errors.New("privacy must be public, unlisted or private")
	}
	return nil
}

// Update applies an Edit and returns the read-back broadcast.
func (c *Controller) Update(ctx context.Context, id string, edit Edit) (youtube.Broadcast, error) {
	if err := edit.Validate(); err != nil {
		return youtube.Broadcast{}, err
	}
	var result youtube.Broadcast
	err := c.run(ctx, "web_update", func(ctx context.Context) error {
		b, err := c.updateBroadcast(ctx, id, func(b *youtube.Broadcast) {
			b.Title = strings.TrimSpace(edit.Title)
			b.Description = edit.Description
			b.ScheduledStart = edit.Start
			b.PrivacyStatus = edit.Privacy
		})
		if err != nil {
			return err
		}
		if edit.CategoryID != "" {
			if err := c.setCategory(ctx, id, edit.CategoryID); err != nil {
				return err
			}
		}
		if edit.StreamID != "" && edit.StreamID != b.BoundStreamID {
			if err := c.bind(ctx, id, edit.StreamID); err != nil {
				return err
			}
			var ok bool
			b, ok, err = c.yt.GetBroadcast(ctx, id)
			if err != nil || !ok {
				return fmt.Errorf("read back after bind: %w", err)
			}
			c.applyAndPublish(ctx, b)
		}
		result = b
		return nil
	})
	return result, err
}

// NewBroadcast is a request to schedule a stream.
type NewBroadcast struct {
	Edit
	Thumbnail     []byte
	ThumbnailType string
}

// Create schedules a broadcast; like Schedule it never changes the selection.
func (c *Controller) Create(ctx context.Context, req NewBroadcast) (youtube.Broadcast, error) {
	if err := req.Validate(); err != nil {
		return youtube.Broadcast{}, err
	}
	var result youtube.Broadcast
	err := c.run(ctx, "web_create", func(ctx context.Context) error {
		b, err := c.createBroadcast(ctx, req)
		result = b
		return err
	})
	return result, err
}

func (c *Controller) createBroadcast(ctx context.Context, req NewBroadcast) (youtube.Broadcast, error) {
	if err := req.Validate(); err != nil {
		return youtube.Broadcast{}, err
	}
	if req.StreamID == "" {
		return youtube.Broadcast{}, errors.New("a stream key is required")
	}
	created, err := c.yt.InsertBroadcast(ctx, youtube.NewBroadcast{
		Title:       strings.TrimSpace(req.Title),
		Description: req.Description,
		Start:       req.Start,
		Privacy:     req.Privacy,
	})
	if err != nil {
		c.log.Error("operation_failed", "operation", "create_insert", "error", err)
		return youtube.Broadcast{}, err
	}
	c.log.Info("broadcast_created", "id", created.ID, "title", created.Title, "scheduledStart", created.ScheduledStart, "privacy", created.PrivacyStatus)
	if err := c.bind(ctx, created.ID, req.StreamID); err != nil {
		return youtube.Broadcast{}, err
	}
	if req.CategoryID != "" {
		if err := c.setCategory(ctx, created.ID, req.CategoryID); err != nil {
			return youtube.Broadcast{}, fmt.Errorf("broadcast created but setting the category failed: %w", err)
		}
	}
	if len(req.Thumbnail) > 0 {
		if err := c.yt.SetThumbnail(ctx, created.ID, req.ThumbnailType, req.Thumbnail); err != nil {
			c.log.Error("operation_failed", "operation", "thumbnail_upload", "id", created.ID, "error", err)
			return youtube.Broadcast{}, fmt.Errorf("broadcast created but thumbnail upload failed: %w", err)
		}
	}
	readback, ok, err := c.yt.GetBroadcast(ctx, created.ID)
	if err != nil || !ok {
		return youtube.Broadcast{}, fmt.Errorf("read back after create: %w", err)
	}
	c.mu.Lock()
	c.session.apply(readback)
	c.mu.Unlock()
	c.pub.update(ctx, c.snapshot)
	return readback, nil
}

// Delete removes a broadcast; a live one is refused.
func (c *Controller) Delete(ctx context.Context, id string) error {
	if b, ok := c.cached(id); ok && isLive(b) {
		return errors.New("the broadcast is live; end the stream before deleting it")
	}
	return c.run(ctx, "web_delete", func(ctx context.Context) error { return c.deleteBroadcast(ctx, id) })
}

// SetThumbnail uploads a thumbnail for a broadcast.
func (c *Controller) SetThumbnail(ctx context.Context, id, contentType string, image []byte) error {
	return c.run(ctx, "web_thumbnail", func(ctx context.Context) error {
		if err := c.yt.SetThumbnail(ctx, id, contentType, image); err != nil {
			c.log.Error("operation_failed", "operation", "thumbnail_upload", "id", id, "error", err)
			return err
		}
		c.log.Info("thumbnail_uploaded", "id", id)
		if b, ok, err := c.yt.GetBroadcast(ctx, id); err == nil && ok {
			c.applyAndPublish(ctx, b)
		}
		return nil
	})
}

func (c *Controller) setCategory(ctx context.Context, id, categoryID string) error {
	if err := c.yt.SetVideoCategory(ctx, id, categoryID); err != nil {
		c.log.Error("operation_failed", "operation", "set_category", "id", id, "categoryId", categoryID, "error", err)
		return fmt.Errorf("set category: %w", err)
	}
	c.log.Info("category_set", "id", id, "categoryId", categoryID)
	return nil
}

func (c *Controller) bind(ctx context.Context, id, streamID string) error {
	if err := c.yt.Bind(ctx, id, streamID); err != nil {
		c.log.Error("operation_failed", "operation", "bind", "id", id, "streamId", streamID, "error", err)
		return fmt.Errorf("bind stream key: %w", err)
	}
	c.log.Info("broadcast_bound", "id", id, "streamId", streamID)
	return nil
}
