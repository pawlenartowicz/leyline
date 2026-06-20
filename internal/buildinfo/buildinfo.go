// Package buildinfo holds the version string shared by every leyline binary
// (leyline, leyline-server, leyline-admin, leyline-web). Value is "dev" for local
// `go build`; release builds inject the tag via
// -ldflags "-X github.com/pawlenartowicz/leyline/internal/buildinfo.Value=<tag>".
package buildinfo

// Value is the leyline version. Overridden at link time for releases.
var Value = "dev"
