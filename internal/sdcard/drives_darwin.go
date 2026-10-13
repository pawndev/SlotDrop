//go:build darwin

package sdcard

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// On macOS every mounted disk is a folder in /Volumes. diskutil says which
// ones are removable, and ejects them.
const diskutil = "/usr/sbin/diskutil"

// mountPoint returns the folder the disk holding path is mounted on, or ""
// if path can't be read.
func mountPoint(path string) string {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return ""
	}
	return unix.ByteSliceToString(st.Mntonname[:])
}

// IsSystemDrive reports whether path is on the disk macOS runs from: its
// read-only system volume, or the Data volume that holds the home folders.
func IsSystemDrive(path string) bool {
	mp := mountPoint(path)
	return mp == "/" || strings.HasPrefix(mp, "/System/Volumes/")
}

type volume struct {
	Name      string
	Removable bool // an SD card, USB stick or external disk
	Ejectable bool
}

// volumeInfo asks diskutil about the disk mounted on mp.
func volumeInfo(mp string) (volume, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, diskutil, "info", "-plist", mp).Output()
	if err != nil {
		return volume{}, false
	}
	kv := plistDict(out)
	return volume{
		Name:      kv["VolumeName"],
		Removable: kv["RemovableMediaOrExternalDevice"] == "true" || kv["Removable"] == "true" || kv["Ejectable"] == "true",
		Ejectable: kv["Ejectable"] == "true",
	}, true
}

// plistDict reads the top-level strings and booleans of an XML property
// list, like diskutil's -plist output. Booleans come back as "true"/"false".
func plistDict(data []byte) map[string]string {
	m := map[string]string{}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, key := 0, ""
	for {
		tok, err := d.Token()
		if err != nil {
			return m
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth != 2 { // <plist> is 1, its <dict> is 2
				depth++
				continue
			}
			switch t.Name.Local {
			case "key":
				_ = d.DecodeElement(&key, &t)
			case "string":
				var s string
				_ = d.DecodeElement(&s, &t)
				m[key] = s
			case "true", "false":
				m[key] = t.Name.Local
				_ = d.Skip()
			default:
				_ = d.Skip()
			}
		case xml.EndElement:
			depth--
		}
	}
}

// IsRemovable reports whether path is on a removable disk, like an SD card.
func IsRemovable(path string) bool {
	mp := mountPoint(path)
	if mp == "" || IsSystemDrive(mp) {
		return false
	}
	v, ok := volumeInfo(mp)
	return ok && v.Removable
}

// Drives lists the disks in /Volumes, Slot cards first.
func Drives() []Drive {
	entries, err := os.ReadDir("/Volumes")
	if err != nil {
		return nil
	}
	var slot, other []Drive
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		root := filepath.Join("/Volumes", e.Name())
		// Skips the link to the startup disk and anything that isn't a disk.
		if mountPoint(root) != root {
			continue
		}
		v, ok := volumeInfo(root)
		if !ok { // a network share, or a disk diskutil doesn't know
			continue
		}
		d := Drive{Path: root, Name: v.Name, Removable: v.Removable, IsSlot: IsSlotCard(root)}
		if d.IsSlot {
			slot = append(slot, d)
		} else if d.Removable {
			other = append(other, d)
		}
	}
	return append(slot, other...)
}

// CanEject reports whether root is a whole removable disk (like
// /Volumes/SLOT) that can be ejected.
func CanEject(root string) bool {
	root = filepath.Clean(root)
	if !strings.HasPrefix(root, "/Volumes/") || mountPoint(root) != root {
		return false
	}
	v, ok := volumeInfo(root)
	return ok && (v.Ejectable || v.Removable)
}

// Eject flushes and unmounts the card, then ejects it, like Finder does.
func Eject(root string) error {
	root = filepath.Clean(root)
	if !CanEject(root) {
		return errors.New("only a whole disk can be ejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, diskutil, "eject", root).CombinedOutput()
	if err != nil {
		if strings.Contains(strings.ToLower(string(out)), "dissent") {
			return errors.New("Something is still using the card. Close any windows or programs showing it and try again.")
		}
		return errors.New("macOS wouldn't let the app eject the card. Eject it in Finder instead.")
	}
	return nil
}
