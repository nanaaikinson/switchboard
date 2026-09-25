package linux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalLeaks(t *testing.T) {
	tests := []struct {
		hosts string
		leaks bool
	}{
		{"files mdns4_minimal [NOTFOUND=return] dns", false},
		{"files mdns_minimal [NOTFOUND=return] dns mdns", false},
		{"files resolve [!UNAVAIL=return] dns", false},
		{"files myhostname dns", true},
		{"files dns mdns4_minimal [NOTFOUND=return]", true},
		{"files mdns4 dns", true}, // no [NOTFOUND=return]: misses go on to dns
		{"files", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := localLeaks(tt.hosts); got != tt.leaks {
			t.Errorf("localLeaks(%q) = %v, want %v", tt.hosts, got, tt.leaks)
		}
	}
}

func TestCheckLocalDNS(t *testing.T) {
	tests := []struct {
		name    string
		content string // "" means no file
		wantErr string
	}{
		{name: "nss-mdns", content: "passwd: files\nhosts:  files mdns4_minimal [NOTFOUND=return] dns # comment\n"},
		{name: "dns only", content: "hosts: files dns\n", wantErr: "(hosts: files dns) passes .local names"},
		{name: "missing", wantErr: "is missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.content != "" {
				if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "etc", "nsswitch.conf"), []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := New(Options{Root: root}).CheckLocalDNS(context.Background())
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("CheckLocalDNS: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
