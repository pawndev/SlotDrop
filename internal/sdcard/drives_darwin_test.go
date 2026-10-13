//go:build darwin

package sdcard

import "testing"

func TestPlistDict(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Ejectable</key>
	<true/>
	<key>Stats</key>
	<dict>
		<key>VolumeName</key>
		<string>not this one</string>
	</dict>
	<key>Internal</key>
	<false/>
	<key>TotalSize</key>
	<integer>63864569856</integer>
	<key>VolumeName</key>
	<string>SLOT</string>
</dict>
</plist>`)
	kv := plistDict(data)
	if kv["Ejectable"] != "true" || kv["Internal"] != "false" || kv["VolumeName"] != "SLOT" {
		t.Fatalf("got %v", kv)
	}
	if _, ok := kv["TotalSize"]; ok {
		t.Fatalf("numbers aren't read: %v", kv)
	}
}

func TestSystemDrive(t *testing.T) {
	if !IsSystemDrive("/") || !IsSystemDrive(t.TempDir()) {
		t.Fatal("the startup disk and its Data volume are the system drive")
	}
	if IsRemovable("/") || CanEject("/") {
		t.Fatal("the startup disk isn't removable")
	}
}
