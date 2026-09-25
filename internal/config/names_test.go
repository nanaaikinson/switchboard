package config

import "testing"

func TestQualifyName(t *testing.T) {
	tlds := []string{"test", "internal"}
	tests := []struct{ in, want string }{
		{"myapp", "myapp.test"},
		{"api.myapp", "api.myapp.test"},
		{"*.tenants.myapp", "*.tenants.myapp.test"},
		{"myapp.test", "myapp.test"},
		{"MyApp.Test.", "myapp.test"},
		{"svc.internal", "svc.internal"},
		{"attest", "attest.test"}, // suffix without a dot is not the TLD
		{"myapp.local", "myapp.local.test"},
		{" spaced ", "spaced.test"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := QualifyName(tt.in, tlds); got != tt.want {
			t.Errorf("QualifyName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := QualifyName("myapp", nil); got != "myapp" {
		t.Errorf("QualifyName with no TLDs = %q, want unchanged", got)
	}
}

func TestValidHostname(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"myapp.test", true},
		{"a-b.c1.test", true},
		{"*.myapp.test", true},
		{"test", true},
		{"", false},
		{"*.", false},
		{"a.*.test", false},
		{"-a.test", false},
		{"a-.test", false},
		{"a..test", false},
		{"My.test", false}, // must already be lowercase
		{"a_b.test", false},
		{"a b.test", false},
	}
	for _, tt := range tests {
		if got := ValidHostname(tt.in); got != tt.want {
			t.Errorf("ValidHostname(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
