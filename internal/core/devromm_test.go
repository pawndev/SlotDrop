//go:build devromm

// Save sync against a real RomM. Not part of `go test ./...`: it needs a
// throwaway RomM (it deletes every save of the user it signs in as) with
// "The Nuzlomizer (3.0.0).gba" and "Pokemon - FireRed Version (USA).zip" in
// its GBA library, like grout's dev/romm. Run:
//
//	SLOTDROP_DEV_ROMM=http://localhost:8090 SLOTDROP_DEV_USER=admin SLOTDROP_DEV_PASSWORD=... \
//	  go test -tags devromm -run TestDevRomM -v ./internal/core/
package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"slotlabelmaker/internal/platforms"
	"slotlabelmaker/internal/sdcard"
)

const (
	nuzlo   = "The Nuzlomizer (3.0.0)"
	firered = "Pokemon - FireRed Version (USA)"
)

// devRomM is a plain client for the server, standing in for another device
// (grout on a handheld, say).
type devRomM struct {
	t               *testing.T
	url, user, pass string
	romIDs          map[string]int
}

func (d *devRomM) do(method, path string, body io.Reader, contentType string, out any) int {
	req, _ := http.NewRequest(method, d.url+path, body)
	req.SetBasicAuth(d.user, d.pass)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			d.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	if resp.StatusCode >= 300 && resp.StatusCode != 409 {
		d.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

func (d *devRomM) jsonBody(v any) io.Reader { b, _ := json.Marshal(v); return bytes.NewReader(b) }

// upload puts a save in the autosave slot as another device would.
func (d *devRomM) upload(device, game, content string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("saveFile", game+".srm")
	part.Write([]byte(content))
	w.Close()
	path := fmt.Sprintf("/api/saves?rom_id=%d&slot=autosave&emulator=mGBA&device_id=%s&overwrite=true", d.romIDs[game], device)
	d.do("POST", path, &buf, w.FormDataContentType(), nil)
}

func (d *devRomM) latest(game string) (id int, content string) {
	var saves []struct {
		ID        int     `json:"id"`
		Slot      *string `json:"slot"`
		UpdatedAt string  `json:"updated_at"`
	}
	d.do("GET", fmt.Sprintf("/api/saves?rom_id=%d&slot=autosave", d.romIDs[game]), nil, "", &saves)
	best := ""
	for _, s := range saves {
		if s.Slot != nil && *s.Slot == "autosave" && s.UpdatedAt > best {
			best, id = s.UpdatedAt, s.ID
		}
	}
	if id == 0 {
		return 0, ""
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/saves/%d/content?optimistic=false", d.url, id), nil)
	req.SetBasicAuth(d.user, d.pass)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return id, string(data)
}

func devCard(t *testing.T, nuzloSave string) string {
	root := t.TempDir()
	for _, dir := range []string{"Games/GBA", "System", "Saves/GBA"} {
		os.MkdirAll(filepath.Join(root, dir), 0o755)
	}
	os.WriteFile(filepath.Join(root, "System/slot"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "Games/GBA", nuzlo+".gba"), []byte("rom"), 0o644)
	os.WriteFile(filepath.Join(root, "Games/GBA", firered+".gba"), []byte("rom"), 0o644)
	if nuzloSave != "" {
		os.WriteFile(filepath.Join(root, "Saves/GBA", nuzlo+".sav"), []byte(nuzloSave), 0o644)
	}
	return root
}

func cardSave(t *testing.T, root, game string) string {
	data, _ := os.ReadFile(sdcard.SavePath(root, platforms.GBA, game))
	return string(data)
}

func TestDevRomM(t *testing.T) {
	url := os.Getenv("SLOTDROP_DEV_ROMM")
	if url == "" {
		t.Skip("SLOTDROP_DEV_ROMM isn't set")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	d := &devRomM{t: t, url: url, user: os.Getenv("SLOTDROP_DEV_USER"), pass: os.Getenv("SLOTDROP_DEV_PASSWORD"), romIDs: map[string]int{}}

	// A clean slate: no saves for this user.
	var roms struct {
		Items []struct {
			ID          int    `json:"id"`
			FsNameNoExt string `json:"fs_name_no_ext"`
		} `json:"items"`
	}
	d.do("GET", "/api/roms?limit=100", nil, "", &roms)
	for _, r := range roms.Items {
		d.romIDs[r.FsNameNoExt] = r.ID
	}
	if d.romIDs[nuzlo] == 0 || d.romIDs[firered] == 0 {
		t.Fatalf("the dev RomM needs %s and %s, has %v", nuzlo, firered, d.romIDs)
	}
	var all []struct {
		ID int `json:"id"`
	}
	d.do("GET", "/api/saves", nil, "", &all)
	if len(all) > 0 {
		ids := []int{}
		for _, s := range all {
			ids = append(ids, s.ID)
		}
		d.do("POST", "/api/saves/delete", d.jsonBody(map[string]any{"saves": ids}), "application/json", nil)
	}
	run := time.Now().Format("150405.000")

	// Another device (a handheld) already has a FireRed save in RomM.
	var other struct {
		DeviceID string `json:"device_id"`
	}
	d.do("POST", "/api/devices", d.jsonBody(map[string]any{"name": "handheld", "platform": "test", "client": "test", "allow_duplicate": true}), "application/json", &other)
	d.upload(other.DeviceID, firered, "firered from handheld "+run)

	// 1. Card A, first sync: its Nuzlomizer save goes up, FireRed's comes down.
	rootA := devCard(t, "nuzlo on card A "+run)
	a := New()
	if _, err := a.OpenCard(rootA); err != nil {
		t.Fatal(err)
	}
	if err := a.Connect(url, d.user, d.pass, ""); err != nil {
		t.Fatal(err)
	}
	sync := func(c *Core, what string) *SyncResult {
		t.Helper()
		res, err := c.SyncSaves(func(int, int) {})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		t.Logf("%s: up %v, down %v, same %d, conflicts %+v, problems %v", what, res.Uploaded, res.Downloaded, res.Unchanged, res.Conflicts, res.Problems)
		if len(res.Problems) > 0 {
			t.Fatalf("%s: problems %v", what, res.Problems)
		}
		return res
	}
	res := sync(a, "first sync")
	if len(res.Uploaded) != 1 || len(res.Downloaded) != 1 || len(res.Conflicts) != 0 {
		t.Fatal("want one upload and one download")
	}
	if got := cardSave(t, rootA, firered); got != "firered from handheld "+run {
		t.Fatalf("FireRed on the card: %q", got)
	}
	if _, got := d.latest(nuzlo); got != "nuzlo on card A "+run {
		t.Fatalf("Nuzlomizer in RomM: %q", got)
	}

	// 2. Nothing changed: nothing happens.
	res = sync(a, "second sync")
	if len(res.Uploaded)+len(res.Downloaded)+len(res.Conflicts) != 0 || res.Unchanged != 2 {
		t.Fatal("want nothing to do")
	}

	// 3. Played on Slot: the new save goes up, even with Slot's clock behind.
	nuzloPath := sdcard.SavePath(rootA, platforms.GBA, nuzlo)
	os.WriteFile(nuzloPath, []byte("nuzlo played on Slot "+run), 0o644)
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(nuzloPath, old, old)
	res = sync(a, "after playing on Slot")
	if len(res.Uploaded) != 1 || len(res.Conflicts) != 0 {
		t.Fatal("want the new save uploaded")
	}
	if _, got := d.latest(nuzlo); got != "nuzlo played on Slot "+run {
		t.Fatalf("Nuzlomizer in RomM: %q", got)
	}

	// 4. Played FireRed on the handheld: it comes down to the card.
	time.Sleep(1100 * time.Millisecond) // RomM's times are to the second
	d.upload(other.DeviceID, firered, "firered played on handheld "+run)
	res = sync(a, "after playing on the handheld")
	if len(res.Downloaded) != 1 || len(res.Conflicts) != 0 {
		t.Fatal("want FireRed downloaded")
	}
	if got := cardSave(t, rootA, firered); got != "firered played on handheld "+run {
		t.Fatalf("FireRed on the card: %q", got)
	}

	// 5. Played on both: a conflict, and nothing is overwritten until you choose.
	time.Sleep(1100 * time.Millisecond)
	d.upload(other.DeviceID, nuzlo, "nuzlo played on handheld "+run)
	os.WriteFile(nuzloPath, []byte("nuzlo played on Slot again "+run), 0o644)
	res = sync(a, "after playing on both")
	if len(res.Conflicts) != 1 || res.Conflicts[0].Stem != nuzlo || len(res.Uploaded)+len(res.Downloaded) != 0 {
		t.Fatal("want one conflict")
	}
	if got := cardSave(t, rootA, nuzlo); got != "nuzlo played on Slot again "+run {
		t.Fatalf("the card's save changed before choosing: %q", got)
	}
	if _, got := d.latest(nuzlo); got != "nuzlo played on handheld "+run {
		t.Fatalf("RomM's save changed before choosing: %q", got)
	}
	cf := res.Conflicts[0]
	if err := a.ResolveSave(cf.Platform, cf.Stem, cf.RomID, cf.SaveID, "card"); err != nil {
		t.Fatal(err)
	}
	if _, got := d.latest(nuzlo); got != "nuzlo played on Slot again "+run {
		t.Fatalf("after keeping the card's, RomM has %q", got)
	}
	res = sync(a, "after keeping the card's")
	if len(res.Uploaded)+len(res.Downloaded)+len(res.Conflicts) != 0 {
		t.Fatal("want nothing to do")
	}

	// 6. Card B, never synced, with its own Nuzlomizer save: never overwritten
	// without asking. Keeping RomM's then puts RomM's on it, with a backup.
	rootB := devCard(t, "nuzlo on card B "+run)
	b := New()
	b.OpenCard(rootB)
	if err := b.Connect(url, d.user, d.pass, ""); err != nil {
		t.Fatal(err)
	}
	res = sync(b, "card B first sync")
	if len(res.Downloaded) != 1 || len(res.Conflicts) != 1 || res.Conflicts[0].Stem != nuzlo {
		t.Fatal("want FireRed downloaded and a Nuzlomizer conflict")
	}
	if got := cardSave(t, rootB, nuzlo); got != "nuzlo on card B "+run {
		t.Fatalf("card B's save changed: %q", got)
	}
	cf = res.Conflicts[0]
	if err := b.ResolveSave(cf.Platform, cf.Stem, cf.RomID, cf.SaveID, "romm"); err != nil {
		t.Fatal(err)
	}
	if got := cardSave(t, rootB, nuzlo); got != "nuzlo played on Slot again "+run {
		t.Fatalf("card B after keeping RomM's: %q", got)
	}
	dir, _ := SaveBackupDir()
	if backups, _ := filepath.Glob(filepath.Join(dir, "GBA", nuzlo, "*")); len(backups) == 0 {
		t.Fatal("card B's own save should be backed up")
	}
	res = sync(b, "card B again")
	if len(res.Uploaded)+len(res.Downloaded)+len(res.Conflicts) != 0 {
		t.Fatal("want nothing to do")
	}
}
