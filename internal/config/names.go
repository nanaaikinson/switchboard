package config

import "strings"

// QualifyName lowercases name, strips a trailing dot, and appends the first of
// tlds unless name already ends with one of them. "myapp" -> "myapp.test",
// "*.tenants.myapp" -> "*.tenants.myapp.test", "api.myapp.test" unchanged.
func QualifyName(name string, tlds []string) string {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if name == "" || len(tlds) == 0 {
		return name
	}
	for _, tld := range tlds {
		if strings.HasSuffix(name, "."+tld) {
			return name
		}
	}
	return name + "." + tlds[0]
}

// ValidHostname reports whether name is a lowercase hostname made of LDH
// labels (letters, digits, hyphens), optionally prefixed by "*." for a wildcard.
func ValidHostname(name string) bool {
	name = strings.TrimPrefix(name, "*.")
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
