package proxy

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/config"
)

func TestAccessLogs(t *testing.T) {
	_, port := upstream(t, "app")
	p, srv := front(t, config.Route{Name: "myapp.test", Port: port, Wildcard: true}, config.Route{Name: "dead.test", Port: 1})

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/login?token=secret", nil)
	req.Host = "a.myapp.test"
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	get(t, srv, "dead.test")
	get(t, srv, "nope.test") // no route: not logged

	logs := p.Logs("myapp.test")
	if len(logs) != 1 {
		t.Fatalf("logs = %+v", logs)
	}
	l := logs[0]
	if l.Method != "POST" || l.Path != "/login" || l.Host != "a.myapp.test" || l.Status != 200 || l.Time.IsZero() {
		t.Errorf("entry %+v; the query string must be dropped", l)
	}
	if d := p.Logs("dead.test"); len(d) != 1 || d[0].Status != http.StatusBadGateway {
		t.Errorf("dead.test logs = %+v", d)
	}
	if n := p.Logs("nope.test"); n == nil || len(n) != 0 {
		t.Errorf("unrouted logs = %#v, want empty, not nil", n)
	}

	// Removing a route drops its log.
	if err := p.SetRoutes([]config.Route{{Name: "myapp.test", Port: port}}); err != nil {
		t.Fatal(err)
	}
	if len(p.Logs("dead.test")) != 0 || len(p.Logs("myapp.test")) != 1 {
		t.Error("logs not pruned to current routes")
	}
}

func TestAccessLogRing(t *testing.T) {
	var l accessLogs
	for i := range logSize + 5 {
		l.add("r", AccessLog{Path: fmt.Sprint(i)})
	}
	got := l.get("r")
	if len(got) != logSize || got[0].Path != "5" || got[logSize-1].Path != fmt.Sprint(logSize+4) {
		t.Errorf("ring kept %d entries from %s to %s", len(got), got[0].Path, got[len(got)-1].Path)
	}
}
