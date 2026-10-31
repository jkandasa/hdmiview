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

// VersionString describes the build, e.g.
// "hdmiview v1.0.0 (commit 1a2b3c4, built 2026-10-31T16:00:00Z, go1.27.0 linux/amd64)".
func VersionString(tool string) string {
	version, commit, date, dirty := Version, Commit, BuildDate, false
	if info, ok := debug.ReadBuildInfo(); ok {
		if version == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if commit == "" {
					commit = s.Value
				}
			case "vcs.time":
				if date == "" {
					date = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	if version == "" {
		version = "dev"
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	if dirty && commit != "" && !strings.HasSuffix(commit, "-dirty") {
		commit += "-dirty"
	}
	var extra []string
	if commit != "" {
		extra = append(extra, "commit "+commit)
	}
	if date != "" {
		extra = append(extra, "built "+date)
	}
	extra = append(extra, runtime.Version()+" "+runtime.GOOS+"/"+runtime.GOARCH)
	return fmt.Sprintf("%s %s (%s)", tool, version, strings.Join(extra, ", "))
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
