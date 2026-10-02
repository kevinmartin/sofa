// Package version exposes the source identity embedded in released CLIs.
package version

// Release builds set these symbols with -ldflags -X. Source builds deliberately
// report development metadata instead of claiming a promoted release.
var Version = "development"
var SourceCommit = "unknown"

type Info struct {
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
}

func Current() Info {
	return Info{
		Version:      Version,
		SourceCommit: SourceCommit,
	}
}

func (i Info) String() string {
	return i.Version + " (" + i.SourceCommit + ")"
}
