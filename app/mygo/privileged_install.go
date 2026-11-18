package main

import (
	"os/exec"
	"strings"
)

// runPrivileged runs the install script with administrator privileges
// through osascript.
func runPrivileged(args []string) error {
	var script strings.Builder
	script.WriteString("on run argv\n")
	script.WriteString(`do shell script "/bin/sh " & quoted form of item 1 of argv`)
	for index := 2; index <= len(args); index++ {
		script.WriteString(" & \" \" & quoted form of item " + itoa(index) + " of argv")
	}
	script.WriteString(" with administrator privileges\nend run")

	commandArgs := append([]string{"-e", script.String()}, args...)
	return exec.Command("/usr/bin/osascript", commandArgs...).Run()
}
