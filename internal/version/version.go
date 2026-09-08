package version

// These values are overridden with -ldflags for release builds.
var (
	Version   = "1.4.8-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

func Get() Info {
	return Info{Name: "jupiq", Version: Version, Commit: Commit, BuildTime: BuildTime}
}
