package store

import "time"

// SetLockBackoff shortens the waits between saves the write gate refused,
// returning the function restoring them.
func SetLockBackoff(d time.Duration) func() {
	old := lockBackoff
	lockBackoff = d
	return func() { lockBackoff = old }
}
