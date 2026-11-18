//go:build darwin

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/getkiko/kiko/app/helperpaths"
)

const (
	// logLimit is the size at which a log under /var/log is rotated.
	logLimit = 32 << 20
	// logCheckInterval is how often the helper checks its logs.
	logCheckInterval = 30 * time.Second
)

// logMu serializes rotation and clearing of the helper-owned logs.
var logMu sync.Mutex

// managedLogs are the only files rotation and clear-logs touch.
func managedLogs() []string {
	return []string{helperPaths.CoreLog, helperPaths.HelperLog}
}

// maintainLogs rotates oversized logs at start and then periodically, so
// the O_APPEND descriptors held by kiko-core and launchd never grow unbounded.
func maintainLogs(ctx context.Context) {
	ticker := time.NewTicker(logCheckInterval)
	defer ticker.Stop()
	for {
		logMu.Lock()
		for _, path := range managedLogs() {
			if _, err := rotateLog(path, logLimit, helperpaths.LogArchives); err != nil {
				fmt.Fprintf(os.Stderr, "rotate %s: %v\n", path, err)
			}
		}
		logMu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// rotateLog copies an oversized log to <path>.1 (shifting older copies up
// to <path>.<keep>) and truncates the live file. Writers holding O_APPEND
// descriptors continue at the new end. A file larger than 4×limit keeps only
// its last limit bytes, starting at a line boundary, so rotation never copies
// gigabytes. It reports whether the file was rotated.
func rotateLog(path string, limit int64, keep int) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() <= limit {
		return false, nil
	}
	size := info.Size()
	if keep > 0 {
		if err := shiftArchives(path, keep); err != nil {
			return false, err
		}
		archive, err := os.OpenFile(path+".1", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0644)
		if err != nil {
			return false, err
		}
		retain := size
		if size > 4*limit {
			retain = limit
		}
		err = copyTail(archive, file, size, retain)
		if closeErr := archive.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return false, err
		}
	}
	return true, file.Truncate(0)
}

// shiftArchives drops <path>.<keep> and renames each older copy one step up,
// leaving <path>.1 free.
func shiftArchives(path string, keep int) error {
	if err := removeRegular(fmt.Sprintf("%s.%d", path, keep)); err != nil {
		return err
	}
	for index := keep - 1; index >= 1; index-- {
		from := fmt.Sprintf("%s.%d", path, index)
		info, err := os.Lstat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", from)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", path, index+1)); err != nil {
			return err
		}
	}
	return nil
}

// copyTail writes the last retain bytes of the first size bytes of src.
// A partial first line is skipped.
func copyTail(dst io.Writer, src *os.File, size, retain int64) error {
	start := size - retain
	if start < 0 {
		start = 0
	}
	reader := bufio.NewReader(io.NewSectionReader(src, start, size-start))
	if start > 0 {
		previous := make([]byte, 1)
		if _, err := src.ReadAt(previous, start-1); err != nil {
			return err
		}
		for previous[0] != '\n' {
			_, err := reader.ReadSlice('\n')
			if err == nil {
				break
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			if !errors.Is(err, bufio.ErrBufferFull) {
				return err
			}
		}
	}
	_, err := io.Copy(dst, reader)
	return err
}

// removeRegular deletes path when it is a regular file; removeRegularSize
// also reports the size it released.
func removeRegular(path string) error {
	_, err := removeRegularSize(path)
	return err
}

func removeRegularSize(path string) (int64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return info.Size(), nil
}

// clearLog truncates the live log and deletes its rotated copies. It reports
// the number of non-empty files cleared and the bytes released.
func clearLog(path string, keep int) (int, int64, error) {
	files, released := 0, int64(0)
	file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return files, released, err
	default:
		info, statErr := file.Stat()
		if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
			statErr = file.Truncate(0)
			if statErr == nil {
				files++
				released += info.Size()
			}
		}
		_ = file.Close()
		if statErr != nil {
			return files, released, statErr
		}
	}
	for index := 1; index <= keep; index++ {
		size, err := removeRegularSize(fmt.Sprintf("%s.%d", path, index))
		if err != nil {
			return files, released, err
		}
		if size > 0 {
			files++
			released += size
		}
	}
	return files, released, nil
}

// clearLogs empties the kiko-core and helper logs under /var/log.
func clearLogs() response {
	logMu.Lock()
	defer logMu.Unlock()
	var res response
	for _, path := range managedLogs() {
		files, released, err := clearLog(path, helperpaths.LogArchives)
		res.Files += files
		res.Bytes += released
		if err != nil {
			res.Error = err.Error()
			return res
		}
	}
	return res
}
