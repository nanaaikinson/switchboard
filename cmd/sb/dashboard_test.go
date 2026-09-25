package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/dashboard"
)

func TestDashboardThroughTheDaemon(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	st := status(t, dir)
	out := mustRun(t, "dashboard", "--print")
	if !strings.Contains(out, "Don't share it.") {
		t.Errorf("no warning about the link:\n%s", out)
	}
	link := strings.Split(out, "\n")[0]
	u, err := url.Parse(link)
	if err != nil || u.Host != "switchboard.test:"+u.Port() || u.Path != "/login" || len(u.Query().Get("token")) != 64 {
		t.Fatalf("link %q: %v", link, err)
	}

	c := httpsClient(t, dir, st.HTTPS.Addrs[0])
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(target, cookie string, hdr ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: dashboard.CookieName, Value: cookie})
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	origin := "https://" + u.Host

	if resp, _ := get(origin+"/", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: %d", resp.StatusCode)
	}
	resp, _ := get(link, "")
	if resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Fatalf("login: %d %v", resp.StatusCode, resp.Cookies())
	}
	session := resp.Cookies()[0].Value
	if resp, _ := get(link, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("reused link: %d", resp.StatusCode)
	}

	resp, body := get(origin+"/", session)
	if resp.StatusCode != 200 || !strings.Contains(body, `<div id="root">`) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("index: %d %s", resp.StatusCode, body)
	}
	if resp, body := get(origin+"/v1/status", session); resp.StatusCode != 200 || !strings.Contains(body, `"tlds":["test"]`) {
		t.Errorf("status via dashboard: %d %s", resp.StatusCode, body)
	}
	if resp, body := get(origin+"/v1/ca", session); resp.StatusCode != 200 || !strings.Contains(body, `"present":true`) || !strings.Contains(body, `"trusted":false`) {
		t.Errorf("ca: %d %s", resp.StatusCode, body) // the test CA isn't in the system store
	}

	// The dashboard name is reserved.
	if _, err := run(t, "add", "switchboard", "3000"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("sb add switchboard: %v", err)
	}
	// Plain HTTP to the dashboard only redirects.
	req, _ := http.NewRequest(http.MethodGet, "http://"+st.Proxy.Addrs[0]+"/", nil)
	req.Host = "switchboard.test"
	plain, err := (&http.Client{CheckRedirect: c.CheckRedirect}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	plain.Body.Close()
	if plain.StatusCode != http.StatusTemporaryRedirect || !strings.HasPrefix(plain.Header.Get("Location"), "https://switchboard.test:") {
		t.Errorf("plain HTTP: %d %s", plain.StatusCode, plain.Header.Get("Location"))
	}
}

func TestDashboardNeedsHTTPS(t *testing.T) {
	configDir(t)
	busy := busyPort(t)
	startDaemon(t, daemonOptions{httpsAddrs: []string{"127.0.0.1:" + strconv.Itoa(busy)}})
	if _, err := run(t, "dashboard", "--print"); err == nil || !strings.Contains(err.Error(), "needs HTTPS") {
		t.Errorf("err = %v", err)
	}
}
