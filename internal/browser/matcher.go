package browser

import (
	"net/url"
	"strings"

	"parental-control-service/internal/config"
)

// ExtractDomain extracts the domain (host without port) from a raw URL string.
// It handles URLs with or without a scheme (http://, https://).
func ExtractDomain(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	// Add scheme if missing so url.Parse works correctly.
	u := rawURL
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}

	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return ""
	}

	host := parsed.Hostname() // strips port
	return strings.ToLower(host)
}

// extractPath extracts the path component from a raw URL string.
// Returns "" if the URL has no path or is invalid.
func extractPath(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	u := rawURL
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}

	return parsed.Path
}

// domainMatches checks whether urlDomain matches the allowed domain,
// considering the IncludeSubdomains flag.
func domainMatches(urlDomain, allowedDomain string, includeSubdomains bool) bool {
	if urlDomain == allowedDomain {
		return true
	}
	if includeSubdomains && strings.HasSuffix(urlDomain, "."+allowedDomain) {
		return true
	}
	return false
}

// IsURLAllowed checks whether the given raw URL matches any entry in the
// allowed sites list.
//
// Matching rules:
//  1. Domain must match (exact or subdomain if IncludeSubdomains=true).
//  2. If AllowedPaths is empty — the entire domain is allowed.
//  3. If AllowedPaths is set — the URL path must start with at least one
//     of the specified prefixes (case-insensitive).
//
// The function checks ALL matching entries. If any entry allows the URL,
// it returns true. This ensures that a specific subdomain entry (e.g.
// drive.google.com with no path restrictions) is not blocked by a parent
// domain entry (e.g. google.com with allowed_paths).
func IsURLAllowed(rawURL string, allowedSites []config.AllowedSite) bool {
	domain := ExtractDomain(rawURL)
	if domain == "" {
		return false
	}

	// Встроенные системные домены — всегда разрешены.
	if isBuiltinSystemDomain(domain) {
		return true
	}

	urlPath := extractPath(rawURL)

	for _, site := range allowedSites {
		allowed := strings.ToLower(site.Domain)
		if !domainMatches(domain, allowed, site.IncludeSubdomains) {
			continue
		}

		// Domain matches. Check path restrictions.
		if len(site.AllowedPaths) == 0 {
			return true // entire domain is allowed
		}

		lowerPath := strings.ToLower(urlPath)
		for _, prefix := range site.AllowedPaths {
			p := strings.ToLower(prefix)
			if strings.HasPrefix(lowerPath, p) {
				return true
			}
		}
	}
	return false
}

// IsSystemSite проверяет, является ли URL системным (не считается развлечением).
func IsSystemSite(rawURL string, allowedSites []config.AllowedSite) bool {
	domain := ExtractDomain(rawURL)
	if domain == "" {
		return false
	}

	if isBuiltinSystemDomain(domain) {
		return true
	}

	for _, site := range allowedSites {
		allowed := strings.ToLower(site.Domain)
		if !domainMatches(domain, allowed, site.IncludeSubdomains) {
			continue
		}
		if site.Category == "system" {
			return true
		}
	}
	return false
}

// isBuiltinSystemDomain возвращает true для встроенных системных доменов.
func isBuiltinSystemDomain(domain string) bool {
	switch domain {
	case "127.0.0.1", "localhost", "::1", "0.0.0.0":
		return true
	}
	return false
}
