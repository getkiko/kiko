package main

import (
	"context"
	"os/exec"
)

// Short-lived checks inherit the desktop's pipes without flashing a console.
func hiddenCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideProcessWindow(cmd)
	return cmd
}
