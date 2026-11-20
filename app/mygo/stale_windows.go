//go:build windows

package main

import (
	"encoding/csv"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// processName is the image name of process pid, as tasklist reports it.
func processName(pid int) (string, error) {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return "", err
	}
	record, err := csv.NewReader(strings.NewReader(string(out))).Read()
	if err != nil || len(record) == 0 {
		return "", errors.New("no such process")
	}
	return record[0], nil
}

func terminateProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid)).Run()
}

func killProcess(pid int) error {
	return exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/F").Run()
}
