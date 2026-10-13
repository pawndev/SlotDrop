//go:build !windows && !darwin

// Package secret protects the saved RomM login. Only Windows and macOS are
// supported; elsewhere nothing is ever saved.
package secret

import "errors"

func Available() bool { return false }

func Protect([]byte) ([]byte, error) {
	return nil, errors.New("saving logins is only supported on Windows and macOS")
}

func Unprotect([]byte) ([]byte, error) {
	return nil, errors.New("saving logins is only supported on Windows and macOS")
}
