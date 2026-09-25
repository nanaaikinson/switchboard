package client_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
)

type nopProxy struct{}

func (nopProxy) SetRoutes([]config.Route) error { return nil }

func TestClientOverUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	svc, err := api.NewService(api.Options{
		ConfigPath: filepath.Join(dir, config.FileName), Config: config.New(),
		Proxy: nopProxy{}, TLDs: []string{"test"}, Version: "v1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, api.SocketName)
	ln, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- api.Serve(ctx, svc, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	c := client.New(sock)
	rs, created, err := c.Put(ctx, config.Route{Name: "myapp", Port: 7000})
	if err != nil || !created || rs.Name != "myapp.test" {
		t.Fatalf("Put = %+v, %v, %v", rs, created, err)
	}
	if _, created, _ := c.Put(ctx, config.Route{Name: "myapp", Port: 7001}); created {
		t.Error("second Put reported created")
	}
	routes, err := c.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Port != 7001 {
		t.Fatalf("Routes = %+v, %v", routes, err)
	}
	st, err := c.Status(ctx)
	if err != nil || st.Version != "v1.0.0" || len(st.Routes) != 1 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	gone, err := c.Delete(ctx, "myapp")
	if err != nil || gone.Name != "myapp.test" {
		t.Fatalf("Delete = %+v, %v", gone, err)
	}
	var se *client.StatusError
	if _, err := c.Delete(ctx, "myapp"); !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Errorf("Delete missing err = %v, want 404 StatusError", err)
	}
}

func TestClientDaemonNotRunning(t *testing.T) {
	dir, err := os.MkdirTemp("", "sb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	c := client.New(filepath.Join(dir, api.SocketName))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Status(ctx); !errors.Is(err, client.ErrDaemonNotRunning) {
		t.Errorf("err = %v, want ErrDaemonNotRunning", err)
	}
}
