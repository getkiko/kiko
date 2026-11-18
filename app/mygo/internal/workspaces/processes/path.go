package processes

import (
	"regexp"
	"strings"
)

var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// PathRegex is the regular expression matching a process's executable
// path exactly, or anything under it as a directory. Windows paths
// (drive letters and UNC) ignore case and accept either separator.
func PathRegex(value string, directory bool) string {
	windows := windowsPath.MatchString(value) || strings.HasPrefix(value, `\\`)
	if !windows {
		suffix := "$"
		if directory {
			suffix = "/"
		}
		return "^" + regexp.QuoteMeta(value) + suffix
	}
	parts := regexp.MustCompile(`[\\/]`).Split(value, -1)
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	suffix := "$"
	if directory {
		suffix = `[\\/]`
	}
	return "(?i)^" + strings.Join(parts, `[\\/]`) + suffix
}
