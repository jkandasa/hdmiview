// Package cli holds what both command-line tools share: version information
// and "--flag" style help output.
package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// Set at build time, e.g.
//
//	go build -ldflags "-X github.com/jkandasa/hdmiview/internal/cli.Version=v1.0.0"
var (
	Version   = ""
	Commit    = ""
	BuildDate = ""
)

// BuildInfo is what --version reports.
type BuildInfo struct {
	Version, Commit, BuildDate, GoVersion, Platform string
}

// Info collects the build information, falling back to what the Go
// toolchain embeds (VCS revision and time) when ldflags were not set.
func Info() BuildInfo {
	b := BuildInfo{Version: Version, Commit: Commit, BuildDate: BuildDate,
		GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	dirty := false
	if info, ok := debug.ReadBuildInfo(); ok {
		if b.Version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			b.Version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if b.Commit == "" {
					b.Commit = s.Value
				}
			case "vcs.time":
				if b.BuildDate == "" {
					b.BuildDate = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	if len(b.Commit) > 7 {
		b.Commit = b.Commit[:7]
	}
	if dirty && b.Commit != "" && !strings.HasSuffix(b.Commit, "-dirty") {
		b.Commit += "-dirty"
	}
	return b
}

// VersionString is the multi-line --version output.
func VersionString(tool string) string {
	b := Info()
	or := func(s string) string {
		if s == "" {
			return "unknown"
		}
		return s
	}
	return fmt.Sprintf("%s %s\n  commit:     %s\n  built:      %s\n  go version: %s\n  platform:   %s",
		tool, b.Version, or(b.Commit), or(b.BuildDate), b.GoVersion, b.Platform)
}

// ShortVersion is a one-line form for logs, e.g. "v1.0.0 (1a2b3c4, go1.27.0)".
func ShortVersion() string {
	b := Info()
	if b.Commit != "" && b.Commit != b.Version {
		return fmt.Sprintf("%s (%s, %s)", b.Version, b.Commit, b.GoVersion)
	}
	return fmt.Sprintf("%s (%s)", b.Version, b.GoVersion)
}

// PrintDefaults writes the flags of fs like flag.PrintDefaults, but with
// the "--name" spelling (Go's flag package accepts both "-name" and "--name").
func PrintDefaults(w io.Writer, fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		typ, usage := flag.UnquoteUsage(f)
		line := "  --" + f.Name
		if typ != "" {
			line += " " + typ
		}
		if def := f.DefValue; def != "" && def != "0" && def != "false" {
			if typ == "string" {
				def = fmt.Sprintf("%q", def)
			}
			usage += " (default " + def + ")"
		}
		fmt.Fprintf(w, "%s\n    \t%s\n", line, strings.ReplaceAll(usage, "\n", "\n    \t"))
	})
}
