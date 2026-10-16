package core

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"slotlabelmaker/internal/romm"
	"slotlabelmaker/internal/sdcard"
)

// syncServer is a fake RomM sync API. Each test says what negotiate answers.
type syncServer struct {
	mu         sync.Mutex
	devices    int
	negotiated []map[string]any
	ops        func(saves []romm.ClientSave) []map[string]any
	uploads    []map[string]string // query, plus "body"
	upload409  bool
	confirms   []map[string]string
	saves      map[int]string // save id -> content
	latest     []map[string]any
	gone       map[string]bool // device ids RomM no longer has
	completed  int
}

const serverTime = "2026-10-01T12:00:00Z"

func newSyncServer(t *testing.T) (*syncServer, *httptest.Server) {
	s := &syncServer{saves: map[int]string{}, gone: map[string]bool{}}
	mux := fakeRomMMux(t)
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["allow_duplicate"] != true || body["client"] != "SlotDrop" {
			t.Errorf("device registration: %v", body)
		}
		s.devices++
		fmt.Fprintf(w, `{"device_id":"dev-%d","name":"Slot SD card","created_at":"x"}`, s.devices)
	})
	mux.HandleFunc("/api/sync/negotiate", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var body struct {
			DeviceID string            `json:"device_id"`
			Saves    []romm.ClientSave `json:"saves"`
			RomIDs   []int             `json:"rom_ids"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if s.gone[body.DeviceID] {
			w.WriteHeader(404)
			w.Write([]byte(`{"detail":"Device with ID x not found"}`))
			return
		}
		s.negotiated = append(s.negotiated, map[string]any{"device": body.DeviceID, "saves": body.Saves, "roms": body.RomIDs})
		json.NewEncoder(w).Encode(map[string]any{"session_id": 3, "operations": s.ops(body.Saves),
			"total_upload": 0, "total_download": 0, "total_conflict": 0, "total_no_op": 0, "total_delete": 0})
	})
	mux.HandleFunc("/api/sync/sessions/3/complete", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.completed++
		s.mu.Unlock()
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/api/saves", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == "GET" {
			json.NewEncoder(w).Encode(s.latest)
			return
		}
		f, hdr, err := r.FormFile("saveFile")
		if err != nil {
			t.Errorf("upload without a file: %v", err)
			return
		}
		data, _ := io.ReadAll(f)
		up := map[string]string{"body": string(data), "file": hdr.Filename}
		for k := range r.URL.Query() {
			up[k] = r.URL.Query().Get(k)
		}
		s.uploads = append(s.uploads, up)
		if s.upload409 && up["overwrite"] != "true" {
			w.WriteHeader(409)
			w.Write([]byte(`{"detail":"Slot has a newer save since your last sync"}`))
			return
		}
		fmt.Fprintf(w, `{"id":90,"rom_id":%s,"file_name":"x.sav","updated_at":"2026-10-02T08:00:00","slot":"autosave"}`, up["rom_id"])
	})
	mux.HandleFunc("/api/saves/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var id int
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			fmt.Sscanf(r.URL.Path, "/api/saves/%d/content", &id)
			if r.URL.Query().Get("optimistic") != "false" || r.URL.Query().Get("device_id") == "" {
				t.Errorf("download query %v", r.URL.Query())
			}
			w.Write([]byte(s.saves[id]))
		case strings.HasSuffix(r.URL.Path, "/downloaded"):
			fmt.Sscanf(r.URL.Path, "/api/saves/%d/downloaded", &id)
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			body["id"] = fmt.Sprint(id)
			s.confirms = append(s.confirms, body)
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	})
	return s, httptest.NewServer(mux)
}

func op(action string, romID, saveID int) map[string]any {
	o := map[string]any{"action": action, "rom_id": romID, "file_name": "f.sav", "slot": "autosave", "reason": "test"}
	if saveID != 0 {
		o["save_id"], o["server_updated_at"], o["server_content_hash"] = saveID, serverTime, "serverhash"
	}
	return o
}

// syncCard is a card with Radical Red (RomM game 1, with a save) and
// Unbound (game 2, no save yet), connected to srv.
func syncCard(t *testing.T, srv *httptest.Server) (*Core, string) {
	t.Setenv("HOME", t.TempDir()) // backups go under the user's config folder
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	root := makeCard(t)
	os.WriteFile(filepath.Join(root, "Games/GBA/Pokemon UnBound.gba"), []byte("unbound rom"), 0o644)
	os.MkdirAll(filepath.Join(root, "Saves/GBA"), 0o755)
	os.WriteFile(filepath.Join(root, "Saves/GBA/Pokemon Radical Red.sav"), []byte("card save"), 0o644)
	c := New()
	if _, err := c.OpenCard(root); err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(srv.URL, "", "", "good-token"); err != nil {
		t.Fatal(err)
	}
	return c, root
}

func TestSyncUploadsAndDownloads(t *testing.T) {
	s, srv := newSyncServer(t)
	defer srv.Close()
	c, root := syncCard(t, srv)
	s.saves[50] = "romm save"
	s.ops = func(saves []romm.ClientSave) []map[string]any {
		return []map[string]any{op("upload", 1, 0), op("download", 2, 50)}
	}
	res, err := c.SyncSaves(func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Uploaded) != 1 || len(res.Downloaded) != 1 || len(res.Conflicts) != 0 || len(res.Problems) != 0 {
		t.Fatalf("result %+v", res)
	}
	sent := s.negotiated[0]["saves"].([]romm.ClientSave)
	if len(sent) != 1 || sent[0].RomID != 1 || sent[0].Slot != "autosave" || sent[0].ContentHash != md5Hex([]byte("card save")) {
		t.Fatalf("negotiated %+v", sent)
	}
	if roms := s.negotiated[0]["roms"].([]int); len(roms) != 2 {
		t.Fatalf("rom scope %v", roms)
	}
	up := s.uploads[0]
	if up["body"] != "card save" || up["slot"] != "autosave" || up["device_id"] != "dev-1" || up["content_hash"] != md5Hex([]byte("card save")) || up["overwrite"] != "" {
		t.Fatalf("upload %v", up)
	}
	path := filepath.Join(root, "Saves/GBA/Pokemon UnBound.sav")
	got, _ := os.ReadFile(path)
	if string(got) != "romm save" {
		t.Fatalf("downloaded %q", got)
	}
	if st, _ := os.Stat(path); !st.ModTime().Equal(romm.ParseTime(serverTime)) {
		t.Errorf("mtime %v, want RomM's time", st.ModTime())
	}
	if len(s.confirms) != 1 || s.confirms[0]["content_hash"] != md5Hex([]byte("romm save")) || s.confirms[0]["device_id"] != "dev-1" {
		t.Fatalf("confirms %v", s.confirms)
	}
	if s.completed != 1 {
		t.Error("the session should be completed")
	}
	st := sdcard.ReadSyncState(root).Servers[srv.URL]
	if st.DeviceID != "dev-1" || st.Saves["1"].Hash != md5Hex([]byte("card save")) || st.Saves["2"].Hash != md5Hex([]byte("romm save")) {
		t.Fatalf("sync state %+v", st)
	}

	// Again: the same device, nothing to do.
	s.ops = func([]romm.ClientSave) []map[string]any { return nil }
	if _, err := c.SyncSaves(func(int, int) {}); err != nil {
		t.Fatal(err)
	}
	if s.devices != 1 || s.negotiated[1]["device"] != "dev-1" || len(s.negotiated[1]["saves"].([]romm.ClientSave)) != 2 {
		t.Fatalf("second sync: %d devices, %v", s.devices, s.negotiated[1])
	}
}

func TestSyncWontOverwriteAnUnsyncedSave(t *testing.T) {
	s, srv := newSyncServer(t)
	defer srv.Close()
	c, root := syncCard(t, srv)
	s.saves[60] = "romm save"
	// No sync history: RomM goes by the clock, which may be wrong on Slot.
	s.ops = func([]romm.ClientSave) []map[string]any { return []map[string]any{op("download", 1, 60)} }
	res, err := c.SyncSaves(func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].SaveID != 60 || res.Conflicts[0].Stem != "Pokemon Radical Red" || len(res.Downloaded) != 0 {
		t.Fatalf("result %+v", res)
	}
	card := filepath.Join(root, "Saves/GBA/Pokemon Radical Red.sav")
	if got, _ := os.ReadFile(card); string(got) != "card save" {
		t.Fatalf("card save was changed: %q", got)
	}

	// You pick RomM's: it's written, and the card's is backed up first.
	if err := c.ResolveSave("GBA", "Pokemon Radical Red", 1, 60, "romm"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(card); string(got) != "romm save" {
		t.Fatalf("card save %q", got)
	}
	dir, _ := SaveBackupDir()
	backups, _ := filepath.Glob(filepath.Join(dir, "GBA", "Pokemon Radical Red", "*.sav"))
	if len(backups) != 1 {
		t.Fatalf("backups %v", backups)
	}
	if got, _ := os.ReadFile(backups[0]); string(got) != "card save" {
		t.Fatalf("backup %q", got)
	}
}

func TestSyncConflictWhenRommMovedOn(t *testing.T) {
	s, srv := newSyncServer(t)
	defer srv.Close()
	c, _ := syncCard(t, srv)
	s.upload409 = true
	s.latest = []map[string]any{
		{"id": 70, "rom_id": 1, "slot": "autosave", "updated_at": "2026-09-01T00:00:00Z"},
		{"id": 77, "rom_id": 1, "slot": "autosave", "updated_at": "2026-09-30T00:00:00Z"},
		{"id": 80, "rom_id": 1, "slot": "other", "updated_at": "2026-10-30T00:00:00Z"},
	}
	s.ops = func([]romm.ClientSave) []map[string]any { return []map[string]any{op("upload", 1, 0)} }
	res, err := c.SyncSaves(func(int, int) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].SaveID != 77 || len(res.Uploaded) != 0 || len(res.Problems) != 0 {
		t.Fatalf("result %+v", res)
	}
	// You pick the card's: uploaded over RomM's.
	if err := c.ResolveSave("GBA", "Pokemon Radical Red", 1, 77, "card"); err != nil {
		t.Fatal(err)
	}
	if last := s.uploads[len(s.uploads)-1]; last["overwrite"] != "true" || last["body"] != "card save" {
		t.Fatalf("upload %v", last)
	}
}

func TestSyncSkipsOtherSlotsAndReregisters(t *testing.T) {
	s, srv := newSyncServer(t)
	defer srv.Close()
	c, root := syncCard(t, srv)
	s.saves[50] = "slot two"
	other := op("download", 2, 50)
	other["slot"] = "slot2"
	s.ops = func([]romm.ClientSave) []map[string]any { return []map[string]any{other} }
	if _, err := c.SyncSaves(func(int, int) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Saves/GBA/Pokemon UnBound.sav")); !os.IsNotExist(err) {
		t.Fatal("a save from another slot shouldn't be put on the card")
	}
	// The card was removed from RomM's device list: it's added again.
	s.gone["dev-1"] = true
	if _, err := c.SyncSaves(func(int, int) {}); err != nil {
		t.Fatal(err)
	}
	if s.devices != 2 || sdcard.ReadSyncState(root).Servers[srv.URL].DeviceID != "dev-2" {
		t.Fatalf("devices %d", s.devices)
	}
}

func TestReportedTimeWithSlowClock(t *testing.T) {
	synced := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := &syncer{server: &sdcard.ServerSync{Saves: map[string]sdcard.SyncedSave{
		"1": {Hash: "old", SyncedAt: synced.Format(time.RFC3339)},
	}}}
	g := &saveGame{romID: 1, hash: "new", mtime: synced.Add(-time.Hour)} // played since, but Slot's clock is behind
	if got := s.reportedTime(g); time.Since(got) > time.Minute {
		t.Fatalf("a changed save should be reported as new, got %v", got)
	}
	g.hash = "old" // unchanged: its own time
	if got := s.reportedTime(g); !got.Equal(g.mtime) {
		t.Fatalf("got %v", got)
	}
}
