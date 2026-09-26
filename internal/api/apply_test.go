package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/config"
)

var (
	fileA = absPath("/work/a/switchboard.toml")
	fileB = absPath("/work/b/switchboard.toml")
)

// absPath makes a slash path absolute on this OS; on Windows "/work" alone
// has no drive letter, so Apply would refuse it.
func absPath(p string) string {
	abs, err := filepath.Abs(filepath.FromSlash(p))
	if err != nil {
		panic(err)
	}
	return abs
}

func rt(name string, port int) config.Route {
	return config.Route{Name: name, Port: port, RedirectHTTPS: true}
}

func names(rs []config.Route) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return strings.Join(out, ",")
}

func mustApply(t *testing.T, s *Service, file string, routes ...config.Route) ApplyResult {
	t.Helper()
	res, err := s.Apply(ApplyRequest{File: file, Routes: routes})
	if err != nil {
		t.Fatalf("Apply(%s): %v", file, err)
	}
	return res
}

func noEvents(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case e := <-ch:
		t.Errorf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	s, fp, path := newTestService(t)
	res := mustApply(t, s, fileA, rt("web", 3000), rt("api.web", 4000))
	if names(res.Added) != "web.test,api.web.test" || len(res.Conflicts) != 0 {
		t.Fatalf("first apply %+v", res)
	}
	saved := loadRoutes(t, path)
	if len(saved) != 2 || saved[0].File != fileA {
		t.Fatalf("saved %+v, want routes tagged with %s", saved, fileA)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)

	events, cancel := s.hub.subscribe()
	defer cancel()
	res = mustApply(t, s, fileA, rt("web", 3000), rt("api.web", 4000))
	if names(res.Unchanged) != "web.test,api.web.test" || len(res.Added)+len(res.Updated)+len(res.Removed) != 0 {
		t.Errorf("second apply %+v, want everything unchanged", res)
	}
	noEvents(t, events)
	after, _ := os.ReadFile(path)
	stat2, _ := os.Stat(path)
	if string(after) != string(before) || !stat2.ModTime().Equal(stat.ModTime()) {
		t.Error("re-applying the same file rewrote routes.toml")
	}
	if names(fp.get()) != "web.test,api.web.test" {
		t.Errorf("proxy routes %s", names(fp.get()))
	}
}

func TestApplyReplacesOnlyThatFilesRoutes(t *testing.T) {
	s, fp, _ := newTestService(t, rt("manual.test", 7000))
	mustApply(t, s, fileA, rt("web", 3000), rt("old", 3001))
	mustApply(t, s, fileB, rt("other", 5000))

	events, cancel := s.hub.subscribe()
	defer cancel()
	res := mustApply(t, s, fileA, rt("web", 3100), rt("new", 3002))
	if names(res.Added) != "new.test" || names(res.Updated) != "web.test" || names(res.Removed) != "old.test" {
		t.Errorf("result %+v", res)
	}
	if got := names(fp.get()); got != "manual.test,other.test,web.test,new.test" {
		t.Errorf("proxy routes %s", got)
	}
	got := map[string]string{}
	for i := 0; i < 3; i++ {
		e := nextEvent(t, events)
		got[e.Route.Name] = e.Type
		if e.Route.Source != SourceFile || e.Route.File != fileA {
			t.Errorf("event %+v, want source file %s", e, fileA)
		}
	}
	if got["old.test"] != EventRouteRemoved || got["new.test"] != EventRouteAdded || got["web.test"] != EventRouteUpdated {
		t.Errorf("events %v", got)
	}
	for _, r := range s.Routes() {
		switch r.Name {
		case "manual.test":
			if r.Source != SourceConfig || r.File != "" {
				t.Errorf("manual route %+v", r)
			}
		case "other.test":
			if r.Source != SourceFile || r.File != fileB {
				t.Errorf("other file's route %+v", r)
			}
		}
	}

	// --down: an empty apply removes exactly that file's routes.
	res = mustApply(t, s, fileA)
	if names(res.Removed) != "web.test,new.test" {
		t.Errorf("down %+v", res)
	}
	if got := names(fp.get()); got != "manual.test,other.test" {
		t.Errorf("after down %s", got)
	}
	if res := mustApply(t, s, fileA); len(res.Removed) != 0 {
		t.Errorf("second down %+v", res)
	}
}

func TestApplyConflictsAreNotOverwritten(t *testing.T) {
	s, fp, path := newTestService(t, rt("manual.test", 7000), config.Route{Name: "wild.test", Port: 7001, Wildcard: true})
	mustApply(t, s, fileB, rt("theirs", 5000))
	s.SetDocker(DockerStatus{Enabled: true, Connected: true}, []DockerRoute{dr("box.test", 8080, "box")})

	res := mustApply(t, s, fileA,
		rt("manual", 1), rt("theirs", 2), rt("box", 3), rt("*.wild", 4), rt("a.wild", 5), rt("fine", 6))
	want := map[string]string{
		"manual.test": "a route added with 'sb add'",
		"theirs.test": "a route from " + fileB,
		"box.test":    "Docker container box",
		"*.wild.test": "a route added with 'sb add'", // wild.test has wildcard = true
	}
	if len(res.Conflicts) != len(want) {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	for _, c := range res.Conflicts {
		if want[c.Name] != c.Owner {
			t.Errorf("conflict %s owner %q, want %q", c.Name, c.Owner, want[c.Name])
		}
	}
	// a.wild.test is an exact name under someone's wildcard: allowed, since
	// exact names win over wildcards.
	if names(res.Added) != "a.wild.test,fine.test" {
		t.Errorf("added %s", names(res.Added))
	}
	for _, r := range loadRoutes(t, path) {
		if (r.Name == "manual.test" && (r.Port != 7000 || r.File != "")) || (r.Name == "theirs.test" && (r.Port != 5000 || r.File != fileB)) {
			t.Errorf("%s was overwritten: %+v", r.Name, r)
		}
	}
	for _, r := range fp.get() {
		if r.Name == "box.test" && r.File != "" {
			t.Errorf("Docker route overwritten: %+v", r)
		}
	}

	// Conflicts stay conflicts on re-apply, and nothing else changes.
	res = mustApply(t, s, fileA, rt("manual", 1), rt("fine", 6), rt("a.wild", 5))
	if len(res.Conflicts) != 1 || len(res.Added)+len(res.Updated) != 0 || len(res.Unchanged) != 2 {
		t.Errorf("re-apply %+v", res)
	}
}

func TestApplyRejectsBadInput(t *testing.T) {
	s, _, path := newTestService(t)
	for name, req := range map[string]ApplyRequest{
		"relative file": {File: "switchboard.toml", Routes: []config.Route{rt("web", 1)}},
		"bad name":      {File: fileA, Routes: []config.Route{rt("not valid", 1)}},
		"bad port":      {File: fileA, Routes: []config.Route{rt("web", 70000)}},
		"proxy port":    {File: fileA, Routes: []config.Route{rt("web", 80)}},
		"https port":    {File: fileA, Routes: []config.Route{rt("web", 443)}},
		"same name":     {File: fileA, Routes: []config.Route{rt("web", 1), rt("web.test", 2)}},
	} {
		if _, err := s.Apply(req); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if len(loadRoutes(t, path)) != 0 {
		t.Error("a rejected apply changed routes.toml")
	}
}

func TestPutTakesOverAFileRoute(t *testing.T) {
	s, _, _ := newTestService(t)
	mustApply(t, s, fileA, rt("web", 3000))
	// sb add on the same name makes it a manual route; the file then conflicts.
	if _, _, err := s.Put(config.Route{Name: "web", Port: 3100, File: fileB}); err != nil {
		t.Fatal(err)
	}
	rs := s.Routes()
	if len(rs) != 1 || rs[0].Source != SourceConfig || rs[0].File != "" {
		t.Fatalf("routes %+v; Put must not accept a file tag", rs)
	}
	res := mustApply(t, s, fileA, rt("web", 3000))
	if len(res.Conflicts) != 1 || len(res.Removed) != 0 {
		t.Errorf("re-apply %+v", res)
	}
}
