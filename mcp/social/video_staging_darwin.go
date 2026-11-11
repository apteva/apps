package main

import "golang.org/x/sys/unix"

func videoStagingSpace(directory string) (uint64, bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(directory, &stat); err != nil {
		return 0, false, err
	}
	return stat.Bavail * uint64(stat.Bsize), unix.ByteSliceToString(stat.Fstypename[:]) == "tmpfs", nil
}
