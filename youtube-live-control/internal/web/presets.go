package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jacobgad/youtube-live-control/internal/preset"
	"github.com/jacobgad/youtube-live-control/internal/store"
	"github.com/jacobgad/youtube-live-control/internal/youtube"
)

type presetsData struct {
	Presets []preset.Preset
	Streams map[string]youtube.Stream
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	presets, err := s.store.ListPresets(ctx)
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
	Images  []store.Image
	IsNew   bool
}

func (s *Server) handlePresetForm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	data := presetFormData{IsNew: true, Preset: preset.Preset{Privacy: "public", Weekday: time.Sunday, TimeOfDay: "09:30", TitleTemplate: "Sunday Service – " + preset.DatePlaceholder}}
	if id := r.PathValue("id"); id != "" {
		p, err := s.store.GetPreset(ctx, id)
		if err != nil {
			s.redirect(w, r, "/presets", "error", "That preset no longer exists.")
			return
		}
		data.Preset, data.IsNew = p, false
	}
	data.Streams = s.streams(ctx)
	data.Images = s.images(ctx)
	s.render(w, r, "preset_form", "Preset", data, "")
}

func (s *Server) images(ctx context.Context) []store.Image {
	images, err := s.store.ListImages(ctx)
	if err != nil {
		s.log.Warn("images_list_failed", "error", err.Error())
	}
	return images
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
		ImageID:       r.FormValue("image_id"),
	}
	imageID, err := s.pickedImage(ctx, r, p.ImageID)
	if err == nil {
		p.ImageID = imageID
		p, err = s.store.SavePreset(ctx, p)
	}
	if err != nil {
		s.render(w, r, "preset_form", "Preset", presetFormData{Preset: p, Streams: s.streams(ctx), Images: s.images(ctx), IsNew: p.ID == ""}, err.Error())
		return
	}
	s.log.Info("preset_saved", "id", p.ID, "name", p.Name)
	s.ctrl.PresetsChanged(ctx)
	s.redirect(w, r, "/presets", "notice", "Saved preset “"+p.Name+"”.")
}

func (s *Server) handleDeletePreset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	id := r.PathValue("id")
	if err := s.store.DeletePreset(ctx, id); err != nil && !errors.Is(err, preset.ErrNotFound) {
		s.redirect(w, r, "/presets", "error", "Could not delete the preset: "+err.Error())
		return
	}
	s.log.Info("preset_deleted", "id", id)
	s.ctrl.PresetsChanged(ctx)
	s.redirect(w, r, "/presets", "notice", "Preset deleted.")
}

func (s *Server) handleDuplicatePreset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	copied, err := s.store.DuplicatePreset(ctx, r.PathValue("id"))
	if err != nil {
		s.redirect(w, r, "/presets", "error", "Could not duplicate the preset: "+err.Error())
		return
	}
	s.log.Info("preset_duplicated", "from", r.PathValue("id"), "id", copied.ID)
	s.ctrl.PresetsChanged(ctx)
	s.redirect(w, r, "/presets/"+copied.ID, "notice", "Duplicated as “"+copied.Name+"” — rename it and adjust what differs.")
}

// An upload joins the library and wins over the radio choice.
func (s *Server) pickedImage(ctx context.Context, r *http.Request, chosen string) (string, error) {
	data, contentType, err := optionalUpload(r, "thumbnail")
	if err != nil {
		return "", err
	}
	if data == nil {
		return chosen, nil
	}
	name := r.FormValue("image_name")
	if name == "" {
		if _, header, err := r.FormFile("thumbnail"); err == nil {
			name = header.Filename
		}
	}
	img, err := s.store.AddImage(ctx, name, contentType, data)
	if err != nil {
		return "", err
	}
	s.log.Info("image_added", "id", img.ID, "name", img.Name, "size", img.Size)
	return img.ID, nil
}

type imagesData struct {
	Images []store.Image
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	s.render(w, r, "images", "Images", imagesData{Images: s.images(ctx)}, "")
}

func (s *Server) handleUploadImage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	if err := r.ParseMultipartForm(maxRequestBytes); err != nil {
		s.redirect(w, r, "/images", "error", "The form could not be read; is the image under 2 MB?")
		return
	}
	data, contentType, err := optionalUpload(r, "thumbnail")
	if err == nil && data == nil {
		err = errors.New("choose a JPEG or PNG file to upload")
	}
	if err != nil {
		s.redirect(w, r, "/images", "error", err.Error())
		return
	}
	name := r.FormValue("image_name")
	if name == "" {
		if _, header, err := r.FormFile("thumbnail"); err == nil {
			name = header.Filename
		}
	}
	img, err := s.store.AddImage(ctx, name, contentType, data)
	if err != nil {
		s.redirect(w, r, "/images", "error", "Could not store the image: "+err.Error())
		return
	}
	s.log.Info("image_added", "id", img.ID, "name", img.Name, "size", img.Size)
	s.redirect(w, r, "/images", "notice", "Added “"+img.Name+"”.")
}

func (s *Server) handleDeleteImage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := requestContext(r)
	defer cancel()
	err := s.store.DeleteImage(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrImageInUse):
		s.redirect(w, r, "/images", "error", "That image is used by a preset; change the preset first.")
	case err != nil && !errors.Is(err, store.ErrImageNotFound):
		s.redirect(w, r, "/images", "error", "Could not delete the image: "+err.Error())
	default:
		s.log.Info("image_deleted", "id", r.PathValue("id"))
		s.redirect(w, r, "/images", "notice", "Image deleted.")
	}
}

func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	data, contentType, err := s.store.ImageBytes(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = w.Write(data)
}
