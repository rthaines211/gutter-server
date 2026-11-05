package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/rthaines211/gutter-server/store"
)

type CreatePresetRequest struct {
	Name             string `json:"name"`
	ReadingMode      string `json:"reading_mode"`
	ReadingDirection string `json:"reading_direction"`
	Theme            string `json:"theme"`
	PageFit          string `json:"page_fit"`
	PageAnimation    string `json:"page_animation"`
	TapZones         bool   `json:"tap_zones"`
	VolumeKeys       bool   `json:"volume_keys"`
	ZoomLock         bool   `json:"zoom_lock"`
}

func (s *Server) HandleListPresets(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-ID")
	if deviceID == "" {
		writeJSONError(w, http.StatusBadRequest, "X-Device-ID header required")
		return
	}
	presets, err := s.Store.ListPresets(deviceID)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if len(presets) == 0 {
		if err := s.Store.SeedDefaultPresets(deviceID); err != nil {
			writeInternalError(w, err)
			return
		}
		presets, err = s.Store.ListPresets(deviceID)
		if err != nil {
			writeInternalError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(presets)
}

func (s *Server) HandleCreatePreset(w http.ResponseWriter, r *http.Request) {
	deviceID := r.Header.Get("X-Device-ID")
	if deviceID == "" {
		writeJSONError(w, http.StatusBadRequest, "X-Device-ID header required")
		return
	}
	var req CreatePresetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}
	id, err := s.Store.CreatePreset(store.ReaderPreset{
		DeviceID: deviceID, Name: req.Name,
		ReadingMode: req.ReadingMode, ReadingDirection: req.ReadingDirection,
		Theme: req.Theme, PageFit: req.PageFit, PageAnimation: req.PageAnimation,
		TapZones: req.TapZones, VolumeKeys: req.VolumeKeys, ZoomLock: req.ZoomLock,
	})
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]int64{"id": id})
}

func (s *Server) HandleUpdatePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req CreatePresetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.Store.UpdatePreset(id, store.ReaderPreset{
		Name: req.Name, ReadingMode: req.ReadingMode, ReadingDirection: req.ReadingDirection,
		Theme: req.Theme, PageFit: req.PageFit, PageAnimation: req.PageAnimation,
		TapZones: req.TapZones, VolumeKeys: req.VolumeKeys, ZoomLock: req.ZoomLock,
	}); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleDeletePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Store.DeletePreset(id); err != nil {
		if err == store.ErrBuiltinPreset {
			writeJSONError(w, http.StatusForbidden, err.Error())
			return
		}
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) HandleResetPreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.Store.ResetPreset(id); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
