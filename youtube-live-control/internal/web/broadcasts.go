package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/controller"
	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

const inputTimeLayout = "2006-01-02T15:04"

type broadcastsData struct {
	Listing controller.Listing
	Presets []preset.Preset
}

func (s *Server) handleBroadcasts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	presets, err := s.store.ListPresets(ctx)
	if err != nil {
		s.log.Error("presets_list_failed", "error", err.Error())
	}
	s.render(w, r, "broadcasts", "Broadcasts", broadcastsData{Listing: s.ctrl.Listing(), Presets: presets}, "")
}

type broadcastForm struct {
	ID           string
	PresetID     string
	Title        string
	Description  string
	Start        time.Time
	Privacy      string
	StreamID     string
	ThumbnailURL string
	Streams      []youtube.Stream
	Lifecycle    string
	Images       []store.Image
	ImageID      string
}

func (s *Server) handleNewBroadcastForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	p, err := s.store.GetPreset(ctx, r.URL.Query().Get("preset"))
	if err != nil {
		s.redirect(w, r, "/broadcasts", "error", "Pick a preset to schedule a new stream.")
		return
	}
	start := p.NextStart(time.Now())
	form := broadcastForm{
		PresetID:    p.ID,
		Title:       p.Title(start),
		Description: p.Description,
		Start:       start,
		Privacy:     p.Privacy,
		StreamID:    p.StreamID,
		ImageID:     p.ImageID,
	}
	form.Streams = s.streams(ctx)
	form.Images = s.images(ctx)
	s.render(w, r, "broadcast_form", "New broadcast", form, "")
}

func (s *Server) handleNewBroadcast(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	form, image, contentType, err := s.parseBroadcastForm(r)
	if err == nil && image == nil && form.ImageID != "" {
		image, contentType, err = s.store.ImageBytes(ctx, form.ImageID)
	}
	if err != nil {
		form.Streams, form.Images = s.streams(ctx), s.images(ctx)
		s.render(w, r, "broadcast_form", "New broadcast", form, err.Error())
		return
	}
	created, err := s.ctrl.Create(ctx, controller.NewBroadcast{Edit: form.edit(), Thumbnail: image, ThumbnailType: contentType})
	if err != nil {
		form.Streams, form.Images = s.streams(ctx), s.images(ctx)
		s.render(w, r, "broadcast_form", "New broadcast", form, err.Error())
		return
	}
	s.redirect(w, r, "/broadcasts", "notice", "Scheduled “"+created.Title+"”.")
}

func (s *Server) handleEditBroadcastForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	b, ok := s.ctrl.Broadcast(ctx, r.PathValue("id"))
	if !ok {
		s.redirect(w, r, "/broadcasts", "error", "That broadcast is no longer on YouTube.")
		return
	}
	form := broadcastForm{
		ID:           b.ID,
		Title:        b.Title,
		Description:  b.Description,
		Start:        b.ScheduledStart,
		Privacy:      b.PrivacyStatus,
		StreamID:     b.BoundStreamID,
		ThumbnailURL: b.ThumbnailURL,
		Lifecycle:    b.LifeCycleStatus,
	}
	form.Streams = s.streams(ctx)
	form.Images = s.images(ctx)
	s.render(w, r, "broadcast_form", "Edit broadcast", form, "")
}

func (s *Server) handleEditBroadcast(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	id := r.PathValue("id")
	form, image, contentType, err := s.parseBroadcastForm(r)
	form.ID = id
	if err == nil && image == nil && form.ImageID != "" {
		image, contentType, err = s.store.ImageBytes(ctx, form.ImageID)
	}
	if err == nil {
		_, err = s.ctrl.Update(ctx, id, form.edit())
	}
	if err == nil && image != nil {
		err = s.ctrl.SetThumbnail(ctx, id, contentType, image)
	}
	if err != nil {
		form.Streams, form.Images = s.streams(ctx), s.images(ctx)
		s.render(w, r, "broadcast_form", "Edit broadcast", form, err.Error())
		return
	}
	s.redirect(w, r, "/broadcasts", "notice", "Saved “"+strings.TrimSpace(form.Title)+"”.")
}

func (s *Server) handleDeleteBroadcast(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	id := r.PathValue("id")
	title := id
	if b, ok := s.ctrl.Broadcast(ctx, id); ok {
		title = b.Title
	}
	if err := s.ctrl.Delete(ctx, id); err != nil {
		s.redirect(w, r, "/broadcasts", "error", "Could not delete “"+title+"”: "+err.Error())
		return
	}
	s.redirect(w, r, "/broadcasts", "notice", "Deleted “"+title+"”.")
}

func (f broadcastForm) edit() controller.Edit {
	return controller.Edit{Title: f.Title, Description: f.Description, Start: f.Start, Privacy: f.Privacy, StreamID: f.StreamID}
}

func (s *Server) parseBroadcastForm(r *http.Request) (broadcastForm, []byte, string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, maxRequestBytes) //nolint:staticcheck // w is not available here; the reader still enforces the cap
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		return broadcastForm{}, nil, "", errors.New("the form could not be read; is the thumbnail under 2 MB?")
	}
	form := broadcastForm{
		PresetID:    r.FormValue("preset_id"),
		Title:       r.FormValue("title"),
		Description: r.FormValue("description"),
		Privacy:     r.FormValue("privacy"),
		StreamID:    r.FormValue("stream_id"),
		ImageID:     r.FormValue("image_id"),
	}
	if raw := r.FormValue("start"); raw != "" {
		start, err := time.ParseInLocation(inputTimeLayout, raw, time.Local)
		if err != nil {
			return form, nil, "", errors.New("scheduled start is not a valid date and time")
		}
		form.Start = start
	}
	image, contentType, err := optionalUpload(r, "thumbnail")
	if err != nil {
		return form, nil, "", err
	}
	return form, image, contentType, nil
}

func optionalUpload(r *http.Request, field string) (image []byte, contentType string, err error) {
	file, header, err := r.FormFile(field)
	if errors.Is(err, http.ErrMissingFile) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", errors.New("the thumbnail could not be read")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, youtube.MaxThumbnailBytes+1))
	if err != nil {
		return nil, "", errors.New("the thumbnail could not be read")
	}
	if len(data) == 0 {
		return nil, "", nil
	}
	if len(data) > youtube.MaxThumbnailBytes {
		return nil, "", errors.New("the thumbnail is larger than YouTube's 2 MB limit")
	}
	contentType = header.Header.Get("Content-Type")
	if contentType != "image/png" && contentType != "image/jpeg" {
		return nil, "", errors.New("thumbnails must be JPEG or PNG")
	}
	return data, contentType, nil
}

func (s *Server) streams(ctx context.Context) []youtube.Stream {
	streams, err := s.ctrl.Streams(ctx)
	if err != nil {
		s.log.Warn("streams_list_failed", "error", err.Error())
	}
	return streams
}
