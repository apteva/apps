//go:build windows

package sdk

// Windows does not expose syscall.Stat_t. Disabling the advisory inode watcher
// is safe: SQLite connection recycling and all database behavior remain active.
func statInode(string) (uint64, bool) { return 0, false }
