package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// SocketName is the control socket's file name inside the config dir.
const SocketName = "sb.sock"

// Handler returns the /v1 control API for s.
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Routes())
	})
	mux.HandleFunc("POST /v1/routes", func(w http.ResponseWriter, r *http.Request) {
		// redirect_https defaults to on, as for 'sb add', project files and
		// Docker; a body that leaves it out keeps that default.
		route := config.Route{RedirectHTTPS: true}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&route); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode route: %w", err))
			return
		}
		rs, created, err := s.Put(route)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		writeJSON(w, code, rs)
	})
	mux.HandleFunc("DELETE /v1/routes/{name}", func(w http.ResponseWriter, r *http.Request) {
		gone, err := s.Delete(r.PathValue("name"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, gone)
	})
	mux.HandleFunc("POST /v1/apply", func(w http.ResponseWriter, r *http.Request) {
		var req ApplyRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode apply request: %w", err))
			return
		}
		res, err := s.Apply(req)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("GET /v1/routes/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		logs, err := s.Logs(r.PathValue("name"))
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, logs)
	})
	mux.HandleFunc("POST /v1/pause", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paused bool `json:"paused"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode pause request: %w", err))
			return
		}
		if err := s.SetPaused(req.Paused); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, req)
	})
	mux.HandleFunc("GET /v1/tlds", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.TLDs())
	})
	mux.HandleFunc("PUT /v1/tlds/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MDNS bool `json:"mdns"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("decode tld request: %w", err))
			return
		}
		created, err := s.AddTLD(r.PathValue("name"), req.MDNS)
		if err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		code := http.StatusOK
		if created {
			code = http.StatusCreated
		}
		writeJSON(w, code, s.TLDs())
	})
	mux.HandleFunc("DELETE /v1/tlds/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.RemoveTLD(r.PathValue("name")); err != nil {
			writeError(w, statusFor(err), err)
			return
		}
		writeJSON(w, http.StatusOK, s.TLDs())
	})
	mux.HandleFunc("GET /v1/ca", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.CA())
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Status())
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		serveEvents(w, r, s.hub)
	})
	return mux
}

// keepalive is how often an idle event stream sends a comment line.
var keepalive = 15 * time.Second

func serveEvents(w http.ResponseWriter, r *http.Request, h *hub) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	events, cancel := h.subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
		case e := <-events:
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
		}
		flusher.Flush()
	}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalid):
		return http.StatusBadRequest
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("api encode response", "err", err)
	}
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, Error{Error: err.Error()})
}

// Serve serves the control API on ln until ctx is done.
func Serve(ctx context.Context, s *Service, ln net.Listener) error {
	return ServeHandler(ctx, Handler(s), ln)
}

// ServeHandler is Serve with a handler built around Handler, such as one
// that adds socket-only endpoints.
func ServeHandler(ctx context.Context, h http.Handler, ln net.Listener) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
	case err := <-errc:
		return fmt.Errorf("api: serve: %w", err)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx) // event streams end via BaseContext cancellation
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("api: serve: %w", err)
	}
	return nil
}
