package helperpaths

import (
	_ "embed"
	"encoding/json"
	"strconv"
)

//go:embed paths.json
var raw []byte

// Version 5 reports reaped core exit codes and termination signals.
// Version 6 adds clear-logs, which empties the logs under /var/log.
const ProtocolVersion = 6

// LogArchives is how many rotated copies (<log>.1 … <log>.N) the helper
// keeps of each log under /var/log.
const LogArchives = 2

// LogFiles is a log followed by its rotated copies, newest first.
func LogFiles(path string) []string {
	files := []string{path}
	for index := 1; index <= LogArchives; index++ {
		files = append(files, path+"."+strconv.Itoa(index))
	}
	return files
}

type Paths struct {
	Label             string `json:"label"`
	Socket            string `json:"socket"`
	HelperExecutable  string `json:"helperExecutable"`
	CoreExecutable    string `json:"coreExecutable"`
	HelperLog         string `json:"helperLog"`
	CoreLog           string `json:"coreLog"`
	LaunchDaemon      string `json:"launchDaemon"`
	UserDataSuffix    string `json:"userDataSuffix"`
	ComponentVersions string `json:"componentVersions"`
}

var Current = func() Paths {
	var paths Paths
	if err := json.Unmarshal(raw, &paths); err != nil {
		panic(err)
	}
	return paths
}()
