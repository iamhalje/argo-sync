package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

// Version must be set via -ldflags '-X'
var (
	Version   = "unknown"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func Short() string {
	parts := []string{
		fmt.Sprintf("version=%s", safe(Version)),
		fmt.Sprintf("commit=%s", safe(Commit)),
		fmt.Sprintf("date=%s", safe(BuildDate)),
		fmt.Sprintf("go=%s", runtime.Version()),
	}
	return strings.Join(parts, " ")
}

func safe(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "unknown"
	}
	return v
}
