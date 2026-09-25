package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// SocketName is the control socket's file name inside the config dir.
const SocketName = "sb.sock"

// maxSocketPath stays under the smallest sun_path limit (104 bytes on macOS).
const maxSocketPath = 100

// DefaultSocketPath returns the control socket path in the config dir.
func DefaultSocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SocketName), nil
}

// Handler returns the /v1 control API for s.
func Handler(s *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Routes())
	})
	mux.HandleFunc("POST /v1/routes", func(w http.ResponseWriter, r *http.Request) {
		var route config.Route
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

// ListenUnix creates the control socket at path with mode 0600. A stale socket
// is replaced; a live one means another daemon is running.
func ListenUnix(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("api: socket path %s is too long (%d > %d bytes); set %s to a shorter directory",
			path, len(path), maxSocketPath, config.EnvConfigDir)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("api: create socket dir: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 500*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("api: a daemon is already running on %s; stop it first", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("api: remove stale socket %s: %w", path, err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("api: listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("api: chmod socket: %w", err)
	}
	return ln, nil
}

// Serve serves the control API on ln until ctx is done.
func Serve(ctx context.Context, s *Service, ln net.Listener) error {
	srv := &http.Server{
		Handler:           Handler(s),
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
