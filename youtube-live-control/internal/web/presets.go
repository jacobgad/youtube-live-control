package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

type presetsData struct {
	Presets []preset.Preset
	Streams map[string]youtube.Stream
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	presets, err := s.presets.List()
	if err != nil {
		s.log.Error("presets_list_failed", "error", err.Error())
	}
	byID := map[string]youtube.Stream{}
	for _, st := range s.streams(ctx) {
		byID[st.ID] = st
	}
	s.render(w, r, "presets", "Presets", presetsData{Presets: presets, Streams: byID}, "")
}

type presetFormData struct {
	Preset  preset.Preset
	Streams []youtube.Stream
	IsNew   bool
}

func (s *Server) handlePresetForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	data := presetFormData{IsNew: true, Preset: preset.Preset{Privacy: "public", Weekday: time.Sunday, TimeOfDay: "09:30", TitleTemplate: "Sunday Service – " + preset.DatePlaceholder}}
	if id := r.PathValue("id"); id != "" {
		p, err := s.presets.Get(id)
		if err != nil {
			s.redirect(w, r, "/presets", "error", "That preset no longer exists.")
			return
		}
		data.Preset, data.IsNew = p, false
	}
	data.Streams = s.streams(ctx)
	s.render(w, r, "preset_form", "Preset", data, "")
}

func (s *Server) handleSavePreset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		s.redirect(w, r, "/presets", "error", "The form could not be read; is the image under 2 MB?")
		return
	}
	weekday, err := strconv.Atoi(r.FormValue("weekday"))
	if err != nil {
		s.redirect(w, r, "/presets", "error", "Choose a weekday.")
		return
	}
	p := preset.Preset{
		ID:            r.PathValue("id"),
		Name:          r.FormValue("name"),
		TitleTemplate: r.FormValue("title_template"),
		Description:   r.FormValue("description"),
		Privacy:       r.FormValue("privacy"),
		StreamID:      r.FormValue("stream_id"),
		Weekday:       time.Weekday(weekday),
		TimeOfDay:     r.FormValue("time_of_day"),
	}
	if p.ID != "" {
		existing, err := s.presets.Get(p.ID)
		if err != nil {
			s.redirect(w, r, "/presets", "error", "That preset no longer exists.")
			return
		}
		p.ThumbnailFile = existing.ThumbnailFile
	}
	image, contentType, err := optionalUpload(r, "thumbnail")
	if err == nil {
		p, err = s.presets.Save(p)
	}
	if err == nil && image != nil {
		ext := ".jpg"
		if contentType == "image/png" {
			ext = ".png"
		}
		_, err = s.presets.SetThumbnail(p.ID, ext, image)
	}
	if err != nil {
		s.render(w, r, "preset_form", "Preset", presetFormData{Preset: p, Streams: s.streams(ctx), IsNew: p.ID == ""}, err.Error())
		return
	}
	s.log.Info("preset_saved", "id", p.ID, "name", p.Name)
	s.ctrl.PresetsChanged()
	s.redirect(w, r, "/presets", "notice", "Saved preset “"+p.Name+"”.")
}

func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.presets.Delete(id); err != nil && !errors.Is(err, preset.ErrNotFound) {
		s.redirect(w, r, "/presets", "error", "Could not delete the preset: "+err.Error())
		return
	}
	s.log.Info("preset_deleted", "id", id)
	s.ctrl.PresetsChanged()
	s.redirect(w, r, "/presets", "notice", "Preset deleted.")
}

func (s *Server) handleDuplicatePreset(w http.ResponseWriter, r *http.Request) {
	copied, err := s.presets.Duplicate(r.PathValue("id"))
	if err != nil {
		s.redirect(w, r, "/presets", "error", "Could not duplicate the preset: "+err.Error())
		return
	}
	s.log.Info("preset_duplicated", "from", r.PathValue("id"), "id", copied.ID)
	s.ctrl.PresetsChanged()
	s.redirect(w, r, "/presets/"+copied.ID, "notice", "Duplicated as “"+copied.Name+"” — rename it and adjust what differs.")
}

func (s *Server) handlePresetThumbnail(w http.ResponseWriter, r *http.Request) {
	image, contentType, ok, err := s.presets.Thumbnail(r.PathValue("id"))
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(image)
}
