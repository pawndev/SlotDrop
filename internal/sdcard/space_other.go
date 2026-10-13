//go:build !windows

package sdcard

import "syscall"

// Space returns the card's total and free bytes.
func Space(root string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

// Drive is a drive that might be the SD card.
type Drive struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Removable bool   `json:"removable"`
	IsSlot    bool   `json:"isSlot"`
	System    bool   `json:"system"`
}
