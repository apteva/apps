//go:build windows

package main

func prepareChromeLaunch(realPath, _ string) (string, []string, error) {
	return realPath, nil, nil
}
