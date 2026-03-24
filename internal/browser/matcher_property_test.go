package browser

import (
	"fmt"
	"testing"

	"parental-control-service/internal/config"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 4: Классификация URL по доменному имени с поддержкой поддоменов и путей
// **Validates: Requirements 8.2, 8.4**

// genDomainName generates a valid domain name like "abcdef.com".
func genDomainName() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		name := rapid.StringMatching(`[a-z]{3,10}`).Draw(t, "name")
		tld := rapid.SampledFrom([]string{"com", "org", "net"}).Draw(t, "tld")
		return name + "." + tld
	})
}

// genSubdomainPrefix generates a random subdomain label like "mail" or "app".
func genSubdomainPrefix() *rapid.Generator[string] {
	return rapid.StringMatching(`[a-z]{2,8}`)
}

// genPathSegment generates a path segment like "/edu" or "/learning/go".
func genPathSegment() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		seg := rapid.StringMatching(`[a-z]{2,8}`).Draw(t, "seg")
		return "/" + seg
	})
}

// TestPropertyURLExactDomainMatch — exact domain match, no path restrictions.
func TestPropertyURLExactDomainMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")
		includeSub := rapid.Bool().Draw(t, "includeSubdomains")

		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: includeSub},
		}

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		path := rapid.SampledFrom([]string{"", "/", "/page", "/path/to/resource"}).Draw(t, "path")
		rawURL := fmt.Sprintf("%s://%s%s", scheme, domain, path)

		result := IsURLAllowed(rawURL, allowedSites)
		if !result {
			t.Fatalf("expected IsURLAllowed=true for URL %q with allowed domain %q, got false", rawURL, domain)
		}
	})
}

// TestPropertyURLSubdomainMatch — subdomain match with include_subdomains=true, no path restrictions.
func TestPropertyURLSubdomainMatch(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")
		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: true},
		}

		subdomain := genSubdomainPrefix().Draw(t, "subdomain")
		fullDomain := subdomain + "." + domain

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		path := rapid.SampledFrom([]string{"", "/", "/page"}).Draw(t, "path")
		rawURL := fmt.Sprintf("%s://%s%s", scheme, fullDomain, path)

		result := IsURLAllowed(rawURL, allowedSites)
		if !result {
			t.Fatalf("expected IsURLAllowed=true for subdomain URL %q with allowed domain %q (include_subdomains=true), got false",
				rawURL, domain)
		}
	})
}

// TestPropertyURLSubdomainDenied — subdomain denied when include_subdomains=false.
func TestPropertyURLSubdomainDenied(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")
		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: false},
		}

		subdomain := genSubdomainPrefix().Draw(t, "subdomain")
		fullDomain := subdomain + "." + domain

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		path := rapid.SampledFrom([]string{"", "/", "/page"}).Draw(t, "path")
		rawURL := fmt.Sprintf("%s://%s%s", scheme, fullDomain, path)

		result := IsURLAllowed(rawURL, allowedSites)
		if result {
			t.Fatalf("expected IsURLAllowed=false for subdomain URL %q with allowed domain %q (include_subdomains=false), got true",
				rawURL, domain)
		}
	})
}

// TestPropertyURLUnrelatedDomain — unrelated domain always denied.
func TestPropertyURLUnrelatedDomain(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain1 := genDomainName().Draw(t, "domain1")
		domain2 := genDomainName().Draw(t, "domain2")

		if domain1 == domain2 {
			domain2 = "zz" + domain2
		}

		includeSub := rapid.Bool().Draw(t, "includeSubdomains")
		allowedSites := []config.AllowedSite{
			{Domain: domain1, IncludeSubdomains: includeSub},
		}

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		path := rapid.SampledFrom([]string{"", "/", "/page"}).Draw(t, "path")
		rawURL := fmt.Sprintf("%s://%s%s", scheme, domain2, path)

		result := IsURLAllowed(rawURL, allowedSites)
		if result {
			t.Fatalf("expected IsURLAllowed=false for URL %q with unrelated allowed domain %q, got true",
				rawURL, domain1)
		}
	})
}

// TestPropertyURLPathAllowed — domain matches and URL path starts with an allowed prefix.
func TestPropertyURLPathAllowed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")
		allowedPath := genPathSegment().Draw(t, "allowedPath")

		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: true, AllowedPaths: []string{allowedPath}},
		}

		// Generate a URL whose path starts with the allowed prefix.
		suffix := rapid.SampledFrom([]string{"", "/sub", "/sub/deep", "/page?q=1"}).Draw(t, "suffix")
		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		rawURL := fmt.Sprintf("%s://%s%s%s", scheme, domain, allowedPath, suffix)

		result := IsURLAllowed(rawURL, allowedSites)
		if !result {
			t.Fatalf("expected IsURLAllowed=true for URL %q with allowed path %q on domain %q, got false",
				rawURL, allowedPath, domain)
		}
	})
}

// TestPropertyURLPathDenied — domain matches but URL path does NOT start with any allowed prefix.
func TestPropertyURLPathDenied(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")
		allowedPath := genPathSegment().Draw(t, "allowedPath")

		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: true, AllowedPaths: []string{allowedPath}},
		}

		// Generate a different path that does NOT start with allowedPath.
		otherSeg := genPathSegment().Draw(t, "otherSeg")
		// Ensure it's actually different.
		if otherSeg == allowedPath {
			otherSeg = "/zzzother"
		}

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		rawURL := fmt.Sprintf("%s://%s%s", scheme, domain, otherSeg)

		result := IsURLAllowed(rawURL, allowedSites)
		if result {
			t.Fatalf("expected IsURLAllowed=false for URL %q with non-matching path (allowed: %q) on domain %q, got true",
				rawURL, allowedPath, domain)
		}
	})
}

// TestPropertyURLPathEmptyMeansFullDomain — when AllowedPaths is empty, any path on the domain is allowed.
func TestPropertyURLPathEmptyMeansFullDomain(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		domain := genDomainName().Draw(t, "domain")

		allowedSites := []config.AllowedSite{
			{Domain: domain, IncludeSubdomains: true, AllowedPaths: []string{}},
		}

		scheme := rapid.SampledFrom([]string{"http", "https"}).Draw(t, "scheme")
		path := genPathSegment().Draw(t, "path")
		suffix := rapid.SampledFrom([]string{"", "/sub", "/a/b/c"}).Draw(t, "suffix")
		rawURL := fmt.Sprintf("%s://%s%s%s", scheme, domain, path, suffix)

		result := IsURLAllowed(rawURL, allowedSites)
		if !result {
			t.Fatalf("expected IsURLAllowed=true for URL %q with empty AllowedPaths on domain %q, got false",
				rawURL, domain)
		}
	})
}
