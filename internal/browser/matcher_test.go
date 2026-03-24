package browser

import (
	"testing"

	"parental-control-service/internal/config"
)

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{"with https", "https://www.google.com/search?q=test", "www.google.com"},
		{"with http", "http://example.org/page", "example.org"},
		{"no scheme", "docs.github.com/en/pages", "docs.github.com"},
		{"with port", "https://localhost:8080/path", "localhost"},
		{"empty string", "", ""},
		{"just domain", "example.com", "example.com"},
		{"uppercase", "HTTPS://Example.COM", "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDomain(tt.rawURL)
			if got != tt.want {
				t.Errorf("ExtractDomain(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestIsURLAllowed(t *testing.T) {
	sites := []config.AllowedSite{
		{Domain: "google.com", IncludeSubdomains: true},
		{Domain: "example.org", IncludeSubdomains: false},
		{Domain: "school.edu", IncludeSubdomains: true},
	}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"exact match", "https://google.com", true},
		{"subdomain allowed", "https://mail.google.com", true},
		{"deep subdomain", "https://a.b.google.com/path", true},
		{"exact no-subdomain", "https://example.org/page", true},
		{"subdomain denied", "https://sub.example.org", false},
		{"unrelated domain", "https://facebook.com", false},
		{"empty url", "", false},
		{"school subdomain", "https://portal.school.edu", true},
		{"no scheme", "google.com/search", true},
		{"partial domain mismatch", "https://notgoogle.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsURLAllowed(tt.url, sites)
			if got != tt.want {
				t.Errorf("IsURLAllowed(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestIsURLAllowed_PathRestrictions(t *testing.T) {
	sites := []config.AllowedSite{
		{
			Domain:            "youtube.com",
			IncludeSubdomains: true,
			AllowedPaths:      []string{"/edu", "/learning/"},
		},
		{
			Domain:            "reddit.com",
			IncludeSubdomains: false,
			AllowedPaths:      []string{"/r/golang", "/r/programming"},
		},
		{
			Domain:            "wikipedia.org",
			IncludeSubdomains: true,
			// AllowedPaths empty — entire domain allowed
		},
	}

	tests := []struct {
		name string
		url  string
		want bool
	}{
		// youtube.com with path restrictions
		{"youtube allowed path exact", "https://youtube.com/edu", true},
		{"youtube allowed path with subpath", "https://youtube.com/edu/math/lesson1", true},
		{"youtube allowed path learning", "https://youtube.com/learning/go", true},
		{"youtube root denied", "https://youtube.com", false},
		{"youtube other path denied", "https://youtube.com/watch?v=123", false},
		{"youtube subdomain allowed path", "https://www.youtube.com/edu/science", true},
		{"youtube subdomain denied path", "https://www.youtube.com/shorts", false},

		// reddit.com with path restrictions (no subdomains)
		{"reddit golang allowed", "https://reddit.com/r/golang", true},
		{"reddit golang subpath", "https://reddit.com/r/golang/comments/123", true},
		{"reddit programming", "https://reddit.com/r/programming", true},
		{"reddit other sub denied", "https://reddit.com/r/funny", false},
		{"reddit root denied", "https://reddit.com", false},
		{"reddit subdomain denied", "https://old.reddit.com/r/golang", false},

		// wikipedia.org — no path restrictions, entire domain allowed
		{"wikipedia any path", "https://wikipedia.org/wiki/Go_(programming_language)", true},
		{"wikipedia subdomain", "https://en.wikipedia.org/wiki/Main_Page", true},

		// Case insensitivity for paths
		{"youtube path case insensitive", "https://youtube.com/EDU/Math", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsURLAllowed(tt.url, sites)
			if got != tt.want {
				t.Errorf("IsURLAllowed(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
