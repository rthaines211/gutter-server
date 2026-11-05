package server

import (
	"encoding/json"
	"net/http"

	"github.com/ryanhaines/comic-reader/rename"
)

var templateSampleFields = map[string]string{
	"series": "Sample Series", "number": "1", "year": "2020",
	"title": "Sample", "format": "TPB", "volume": "Sample Series",
}

func (s *Server) HandleGetRenameTemplates(w http.ResponseWriter, r *http.Request) {
	m, err := s.Store.ListRenameTemplates()
	if err != nil {
		writeInternalError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"templates": m})
}

func (s *Server) HandlePutRenameTemplates(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Templates map[string]string `json:"templates"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Templates) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for format, tpl := range req.Templates {
		if resolved := rename.Resolve(tpl, templateSampleFields); resolved == "" {
			writeJSONError(w, http.StatusBadRequest, "template for '"+format+"' resolves to an empty name")
			return
		}
	}
	if err := s.Store.SaveRenameTemplates(req.Templates); err != nil {
		writeInternalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
