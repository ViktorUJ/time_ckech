// Package version holds build-time version info.
package version

// Set at build time via -ldflags:
//
//	go build -ldflags="-X parental-control-service/internal/version.GitCommit=$(git rev-parse --short HEAD)"
var GitCommit = "dev"
