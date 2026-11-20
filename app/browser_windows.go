package main

import "os"

// endProfileInstance does nothing on Windows, where Chrome's profile lock
// does not name its process.
func endProfileInstance(string) {}

// terminate ends a browser: Windows has no signal to ask it to quit.
func terminate(process *os.Process) { _ = process.Kill() }
