package version

import "fmt"

var (
	ProjectVersion = "dev"
	GitCommit      = "unknown"
	MosdnsBase     = "v5.3.4"
	BuildTime      = "unknown"
)

func Info() map[string]string {
	return map[string]string{
		"version":     ProjectVersion,
		"git_commit":  GitCommit,
		"mosdns_base": MosdnsBase,
		"build_time":  BuildTime,
	}
}

func String() string {
	return fmt.Sprintf("version=%s commit=%s mosdns_base=%s build_time=%s", ProjectVersion, GitCommit, MosdnsBase, BuildTime)
}
