package transport

import (
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/center/CenterSurvey/internal/survey"
	"github.com/mark3labs/mcp-go/server"
)

//go:embed survey.html
var publicHTML string

//go:embed survey.css
var publicCSS string

//go:embed survey.js
var publicJS string

func Handler(s *survey.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", server.NewStreamableHTTPServer(MCP(s)))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.PingContext(r.Context()); err != nil {
			write(w, 503, map[string]string{"status": "unavailable"})
			return
		}
		write(w, 200, map[string]string{"status": "ok", "version": Version})
	})
	for path, asset := range map[string]struct{ kind, body string }{
		"GET /public/surveys/{id}": {"text/html; charset=utf-8", publicHTML},
		"GET /public/survey.css":   {"text/css; charset=utf-8", publicCSS},
		"GET /public/survey.js":    {"text/javascript; charset=utf-8", publicJS},
	} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.PathValue("id") != "" {
				if _, err := s.Get(r.Context(), r.PathValue("id"), true); err != nil {
					fail(w, err)
					return
				}
			}
			w.Header().Set("Content-Type", asset.kind)
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
			w.Header().Set("Referrer-Policy", "no-referrer")
			_, _ = io.WriteString(w, asset.body)
		})
	}
	mux.HandleFunc("GET /api/v1/surveys/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Get(r.Context(), r.PathValue("id"), true)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, v)
	})
	mux.HandleFunc("POST /api/v1/surveys/{id}/responses", func(w http.ResponseWriter, r *http.Request) {
		var in survey.Submission
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&in); err != nil {
			fail(w, survey.ErrInvalid)
			return
		}
		if err := d.Decode(new(any)); err != io.EOF {
			fail(w, survey.ErrInvalid)
			return
		}
		v, err := s.Submit(r.Context(), r.PathValue("id"), r.Header.Get("Idempotency-Key"), in)
		if err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]string{"id": v.ID, "survey_id": v.SurveyID, "created_at": v.CreatedAt})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}
func publicError(err error) string {
	switch {
	case errors.Is(err, survey.ErrInvalid), errors.Is(err, survey.ErrNotFound), errors.Is(err, survey.ErrConflict):
		return err.Error()
	default:
		log.Printf("Survey operation failed: %v", err)
		return "Operation failed. Check the server configuration and try again."
	}
}
func fail(w http.ResponseWriter, err error) {
	status := 500
	switch {
	case errors.Is(err, survey.ErrInvalid):
		status = 400
	case errors.Is(err, survey.ErrNotFound):
		status = 404
	case errors.Is(err, survey.ErrConflict):
		status = 409
	}
	write(w, status, map[string]string{"error": publicError(err)})
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
