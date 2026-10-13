//go:build !windows && !darwin

package sdcard

import "errors"

// IsRemovable is only known on Windows and macOS; elsewhere every folder counts.
func IsRemovable(string) bool { return true }

// IsSystemDrive is only known on Windows and macOS.
func IsSystemDrive(string) bool { return false }

// Drives only means something on Windows and macOS.
func Drives() []Drive { return nil }

// CanEject is only supported on Windows and macOS.
func CanEject(string) bool { return false }

// Eject is only supported on Windows and macOS.
func Eject(string) error { return errors.New("ejecting is only supported on Windows and macOS") }
