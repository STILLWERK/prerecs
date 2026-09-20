//go:build !windows

package main

func vfwCanDecodeFrame(path string, frame int64) (bool, error) {
	return true, nil
}
