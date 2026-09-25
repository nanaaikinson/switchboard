package proxy

import (
	"net/http"
	"sync"
	"time"
)

// logSize is how many recent requests are kept per route.
const logSize = 100

// AccessLog is one proxied request.
type AccessLog struct {
	Time       time.Time `json:"time"`
	Host       string    `json:"host"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`        // without the query string, which may hold secrets
	Status     int       `json:"status"`      // 0 if the client went away first
	DurationMs float64   `json:"duration_ms"` // until the response ended; long for streams and WebSockets
}

// accessLogs keeps the last logSize requests for each route, in memory only.
type accessLogs struct {
	mu     sync.Mutex
	routes map[string]*ring
}

type ring struct {
	buf  [logSize]AccessLog
	next int
	full bool
}

func (l *accessLogs) add(route string, e AccessLog) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.routes == nil {
		l.routes = map[string]*ring{}
	}
	r := l.routes[route]
	if r == nil {
		r = &ring{}
		l.routes[route] = r
	}
	r.buf[r.next] = e
	r.next = (r.next + 1) % logSize
	r.full = r.full || r.next == 0
}

// get returns route's requests, oldest first.
func (l *accessLogs) get(route string) []AccessLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.routes[route]
	if r == nil {
		return []AccessLog{}
	}
	if !r.full {
		return append([]AccessLog(nil), r.buf[:r.next]...)
	}
	return append(append([]AccessLog(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}

// keep drops the logs of routes not in names.
func (l *accessLogs) keep(names map[string]bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for n := range l.routes {
		if !names[n] {
			delete(l.routes, n)
		}
	}
}

// statusRecorder captures the response status. Unwrap lets
// http.ResponseController reach the real writer, so flushing (SSE) and
// hijacking (WebSockets) keep working.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }
