// Package version carries the build identity the daemon prints at startup.
package version

// Version is the release this binary was cut from.
const Version = "0.1.0"

// Commit is the git revision. It is injected at build time with
// -ldflags "-X .../internal/version.Commit=$(git rev-parse --short HEAD)"
// so a running daemon can be tied back to a source tree.
var Commit = "dev"
