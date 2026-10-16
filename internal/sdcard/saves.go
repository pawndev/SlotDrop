package sdcard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"slotlabelmaker/internal/platforms"
)

// Slot keeps a game's battery save as Saves/<GBA|GB|GBC>/<stem>.sav, and
// reads <stem>.srm (RetroArch's name) when there's no .sav. Save states live
// in States/ and are left alone.

// SavePath is where Slot writes a game's save.
func SavePath(root string, p *platforms.Platform, stem string) string {
	return filepath.Join(root, "Saves", p.Key, stem+".sav")
}

// FindSave is the save Slot would load for a game, or "" if it has none.
func FindSave(root string, p *platforms.Platform, stem string) string {
	sav := SavePath(root, p, stem)
	for _, path := range []string{sav, strings.TrimSuffix(sav, ".sav") + ".srm"} {
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			return path
		}
	}
	return ""
}

// SyncState is what SlotDrop remembers on the card about syncing its saves,
// per RomM server: the device RomM knows the card as, and what each save was
// the last time it was synced. It lives on the card, not the PC, because
// the saves do: the same card synced from two PCs is still one device.
type SyncState struct {
	Servers map[string]*ServerSync `json:"servers"`
}

// ServerSync is the card's sync state with one RomM server.
type ServerSync struct {
	DeviceID string `json:"device_id"`
	// Saves is keyed by RomM game id.
	Saves map[string]SyncedSave `json:"saves"`
}

// SyncedSave is a save as it was when last synced.
type SyncedSave struct {
	Hash     string `json:"hash"`      // MD5 of the save, like RomM's content_hash
	SyncedAt string `json:"synced_at"` // RFC 3339
}

func syncStateFile(root string) string { return filepath.Join(root, ".slotdrop", "sync.json") }

// ReadSyncState reads the card's sync state; an empty one if there is none.
func ReadSyncState(root string) *SyncState {
	st := &SyncState{}
	if data, err := os.ReadFile(syncStateFile(root)); err == nil {
		_ = json.Unmarshal(data, st)
	}
	if st.Servers == nil {
		st.Servers = map[string]*ServerSync{}
	}
	return st
}

// Server is the state for one RomM address, made if it's new.
func (s *SyncState) Server(baseURL string) *ServerSync {
	ss := s.Servers[baseURL]
	if ss == nil {
		ss = &ServerSync{}
		s.Servers[baseURL] = ss
	}
	if ss.Saves == nil {
		ss.Saves = map[string]SyncedSave{}
	}
	return ss
}

// WriteSyncState saves the card's sync state.
func WriteSyncState(root string, st *SyncState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileSafely(syncStateFile(root), data)
}
