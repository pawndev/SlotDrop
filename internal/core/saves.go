package core

// Syncing saves between the card and RomM.
//
// RomM does the deciding: SlotDrop tells it which saves the card has (with a
// hash and when each was written) and RomM answers, per game, upload,
// download, conflict or nothing. SlotDrop registers each SD card as its own
// device in RomM, so RomM knows what the card last synced.
//
// A save is the only thing on the card that can't be got back, so:
//   - a save on the card is copied to the PC (Save backups) before it's
//     overwritten or removed;
//   - when both sides changed, or it isn't sure which did, nothing is
//     written until you choose;
//   - only RomM's "autosave" slot is synced, the one grout and other
//     handheld apps use, so other slots and manual uploads are left alone.

import (
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"slotlabelmaker/internal/platforms"
	"slotlabelmaker/internal/romm"
	"slotlabelmaker/internal/sdcard"
)

// SaveConflict is a save SlotDrop won't sync by itself: both copies changed,
// or it can't tell which did. You pick which one to keep.
type SaveConflict struct {
	Platform string `json:"platform"`
	Stem     string `json:"stem"`
	Title    string `json:"title"`
	RomID    int    `json:"romId"`
	SaveID   int    `json:"saveId"`  // RomM's copy, 0 if it was deleted there
	Deleted  bool   `json:"deleted"` // RomM's copy was deleted in RomM
	CardTime string `json:"cardTime,omitempty"`
	CardSize int64  `json:"cardSize"`
	RommTime string `json:"rommTime,omitempty"`
	Reason   string `json:"reason"`
}

// SyncResult is what a sync did.
type SyncResult struct {
	Uploaded   []string       `json:"uploaded"`   // game titles
	Downloaded []string       `json:"downloaded"` // game titles
	Unchanged  int            `json:"unchanged"`
	Conflicts  []SaveConflict `json:"conflicts"`
	Problems   []string       `json:"problems"`
}

// saveGame is a card game paired with a RomM game, and its save if it has one.
type saveGame struct {
	p     *platforms.Platform
	stem  string
	title string
	romID int
	path  string // the save Slot loads, or "" if none
	data  []byte
	hash  string
	mtime time.Time
	skip  bool // its save couldn't be read
}

func md5Hex(data []byte) string { return fmt.Sprintf("%x", md5.Sum(data)) }

func (g *saveGame) key() string { return strconv.Itoa(g.romID) }

// readSave loads the game's save from the card, if it has one.
func (g *saveGame) readSave(root string) error {
	g.path, g.data, g.hash, g.mtime = sdcard.FindSave(root, g.p, g.stem), nil, "", time.Time{}
	if g.path == "" {
		return nil
	}
	f, err := os.Open(g.path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > romm.MaxSaveBytes {
		return fmt.Errorf("its save is %d MB, too big to be a Game Boy save", st.Size()>>20)
	}
	if g.data, err = io.ReadAll(io.LimitReader(f, romm.MaxSaveBytes)); err != nil {
		return err
	}
	g.hash, g.mtime = md5Hex(g.data), st.ModTime()
	return nil
}

// saveGames lists the card games RomM knows, one per RomM game and per save
// file. Games that share either can't be synced safely and are reported.
func (c *Core) saveGames(root string) ([]*saveGame, []string) {
	c.mu.Lock()
	c.ensureMatches()
	type found struct {
		p     *platforms.Platform
		stem  string
		romID int
		title string
	}
	var list []found
	for _, p := range platforms.All {
		for _, g := range sdcard.FindGames(root, p) {
			res := c.matches[key(p, g.Stem)]
			if id, ok := c.manual[key(p, g.Stem)]; ok {
				res.Rom = c.byID[id]
			}
			if res.Rom != nil {
				list = append(list, found{p, g.Stem, res.Rom.ID, res.Rom.DisplayName()})
			}
		}
	}
	c.mu.Unlock()

	var games []*saveGame
	var problems []string
	byRom := map[int]*saveGame{}
	byFile := map[string]bool{}
	for _, f := range list {
		file := key(f.p, f.stem)
		if byFile[file] { // Tetris.gb and Tetris.gbc share one save
			continue
		}
		byFile[file] = true
		if other := byRom[f.romID]; other != nil {
			problems = append(problems, fmt.Sprintf("%s: its save isn't synced, because %s on the card is the same RomM game", f.stem, other.stem))
			continue
		}
		g := &saveGame{p: f.p, stem: f.stem, title: f.title, romID: f.romID}
		byRom[f.romID] = g
		games = append(games, g)
	}
	return games, problems
}

// syncer holds one sync's connection and the card's sync state.
type syncer struct {
	root    string
	client  *romm.Client
	state   *sdcard.SyncState
	server  *sdcard.ServerSync
	session int
}

func (c *Core) newSyncer() (*syncer, error) {
	root, client := c.Root(), c.Client()
	switch {
	case root == "":
		return nil, errors.New("open the SD card first")
	case client == nil:
		return nil, errors.New("connect to RomM first")
	}
	st := sdcard.ReadSyncState(root)
	return &syncer{root: root, client: client, state: st, server: st.Server(client.BaseURL)}, nil
}

// register adds the card to RomM's device list.
func (s *syncer) register() error {
	name := "Slot SD card"
	for _, d := range sdcard.Drives() {
		if filepath.Clean(d.Path) == s.root && d.Name != "" {
			name += " (" + d.Name + ")"
		}
	}
	id, err := s.client.RegisterDevice(name)
	if err != nil {
		return err
	}
	// A new device has synced nothing, so what the card remembers is stale.
	s.server.DeviceID, s.server.Saves = id, map[string]sdcard.SyncedSave{}
	return s.save()
}

func (s *syncer) save() error {
	if err := sdcard.WriteSyncState(s.root, s.state); err != nil {
		return fmt.Errorf("couldn't write SlotDrop's sync notes on the card: %v", err)
	}
	return nil
}

// reportedTime is when the card says a save was written. Slot's clock can be
// off, so a save that changed since the last sync but looks older than it is
// reported as written now: RomM then sees it changed instead of dropping it.
func (s *syncer) reportedTime(g *saveGame) time.Time {
	t := g.mtime.UTC()
	if rec, ok := s.server.Saves[g.key()]; ok && rec.Hash != g.hash {
		if synced := romm.ParseTime(rec.SyncedAt); !t.After(synced) {
			t = time.Now().UTC()
		}
	}
	return t
}

func (s *syncer) record(g *saveGame, hash string, at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	s.server.Saves[g.key()] = sdcard.SyncedSave{Hash: hash, SyncedAt: at.UTC().Format(time.RFC3339)}
}

// cardUnchanged reports whether the card's save is the one last synced.
func (s *syncer) cardUnchanged(g *saveGame) bool {
	rec, ok := s.server.Saves[g.key()]
	return ok && rec.Hash == g.hash
}

func (s *syncer) upload(g *saveGame, overwrite bool) error {
	saved, err := s.client.UploadSave(romm.UploadQuery{
		RomID: g.romID, DeviceID: s.server.DeviceID, ContentHash: g.hash,
		SessionID: s.session, Overwrite: overwrite,
	}, filepath.Base(g.path), g.data)
	if err != nil {
		return err
	}
	s.record(g, g.hash, romm.ParseTime(saved.UpdatedAt))
	return nil
}

// download writes RomM's save onto the card, keeping a copy of the card's
// own save on the PC first.
func (s *syncer) download(g *saveGame, saveID int, serverTime time.Time) error {
	data, err := s.client.DownloadSave(saveID, s.server.DeviceID, s.session)
	if err != nil {
		return err
	}
	if g.path != "" {
		if err := backupSave(g); err != nil {
			return fmt.Errorf("couldn't back up the card's save first, so it was left alone: %v", err)
		}
	}
	target := sdcard.SavePath(s.root, g.p, g.stem)
	if err := sdcard.WriteFileSafely(target, data); err != nil {
		return fmt.Errorf("couldn't write the save to the card: %v", err)
	}
	if !serverTime.IsZero() {
		_ = os.Chtimes(target, serverTime, serverTime)
	}
	hash := md5Hex(data)
	// If this doesn't get through, the next sync sees identical saves anyway.
	_ = s.client.ConfirmDownloaded(saveID, s.server.DeviceID, hash)
	s.record(g, hash, serverTime)
	return nil
}

func (s *syncer) conflict(g *saveGame, saveID int, serverTime, reason string) SaveConflict {
	sc := SaveConflict{
		Platform: g.p.Key, Stem: g.stem, Title: g.title, RomID: g.romID,
		SaveID: saveID, Deleted: saveID == 0, RommTime: serverTime, Reason: reason,
		CardSize: int64(len(g.data)),
	}
	if !g.mtime.IsZero() {
		sc.CardTime = g.mtime.UTC().Format(time.RFC3339)
	}
	return sc
}

func ptr[T any](p *T) (v T) {
	if p != nil {
		v = *p
	}
	return v
}

// negotiateBatch is how many games go to RomM in one negotiation (its limit).
const negotiateBatch = 500

// SyncSaves syncs the saves of every card game RomM knows. progress is told
// how many of the saves to move are done.
func (c *Core) SyncSaves(progress func(done, total int)) (*SyncResult, error) {
	s, err := c.newSyncer()
	if err != nil {
		return nil, err
	}
	games, problems := c.saveGames(s.root)
	res := &SyncResult{Problems: problems}
	if len(games) == 0 {
		return res, nil
	}
	for _, g := range games {
		if err := g.readSave(s.root); err != nil {
			res.Problems = append(res.Problems, g.stem+": "+err.Error())
			g.skip = true
		}
	}
	if s.server.DeviceID == "" {
		if err := s.register(); err != nil {
			return nil, err
		}
	}
	for start := 0; start < len(games); start += negotiateBatch {
		batch := games[start:min(start+negotiateBatch, len(games))]
		if err := s.syncBatch(batch, res, progress); err != nil {
			_ = s.save()
			return nil, err
		}
	}
	return res, s.save()
}

func (s *syncer) syncBatch(batch []*saveGame, res *SyncResult, progress func(done, total int)) error {
	byRom := map[int]*saveGame{}
	var ids []int
	var saves []romm.ClientSave
	for _, g := range batch {
		if g.skip {
			continue
		}
		byRom[g.romID] = g
		ids = append(ids, g.romID)
		if g.path != "" {
			saves = append(saves, romm.ClientSave{
				RomID: g.romID, FileName: filepath.Base(g.path), Slot: romm.SaveSlot, Emulator: romm.SaveEmulator,
				ContentHash: g.hash, UpdatedAt: s.reportedTime(g), Size: int64(len(g.data)),
			})
		}
	}
	if len(ids) == 0 {
		return nil
	}
	plan, err := s.client.Negotiate(s.server.DeviceID, saves, ids)
	if romm.IsStatus(err, 404) { // the card was removed from RomM's device list
		if err = s.register(); err == nil {
			plan, err = s.client.Negotiate(s.server.DeviceID, saves, ids)
		}
	}
	if err != nil {
		var se *romm.StatusError
		if errors.As(err, &se) && se.Code == 400 && strings.Contains(se.Detail, "disabled") {
			return errors.New("save sync is turned off for this SD card in RomM's device list")
		}
		return err
	}
	s.session = plan.SessionID

	// Moves first counted, so the progress bar knows the total.
	var todo []romm.SyncOp
	for _, op := range plan.Operations {
		g := byRom[op.RomID]
		switch {
		case g == nil || op.SlotName() != romm.SaveSlot: // another slot, or a game not on the card
		case op.Action == "no_op":
			res.Unchanged++
			if g.path != "" && g.hash == ptr(op.ServerHash) {
				s.record(g, g.hash, romm.ParseTime(ptr(op.ServerUpdatedAt)))
			}
		default:
			todo = append(todo, op)
		}
	}
	done, failed := 0, 0
	for i, op := range todo {
		progress(i, len(todo))
		g := byRom[op.RomID]
		saveID, serverTime := ptr(op.SaveID), ptr(op.ServerUpdatedAt)
		var err error
		switch op.Action {
		case "upload":
			if g.path == "" {
				continue
			}
			err = s.upload(g, false)
			if romm.IsStatus(err, 409) {
				// RomM's slot moved on since the card last synced.
				err = nil
				latest, lerr := s.client.LatestSave(g.romID)
				if lerr != nil || latest == nil {
					res.Conflicts = append(res.Conflicts, s.conflict(g, 0, "", "RomM has a newer save than this card last saw"))
				} else {
					res.Conflicts = append(res.Conflicts, s.conflict(g, latest.ID, latest.UpdatedAt, "RomM has a newer save than this card last saw"))
				}
				continue
			}
			if err == nil {
				res.Uploaded = append(res.Uploaded, g.title)
			}
		case "download":
			if saveID == 0 {
				continue
			}
			// RomM only knows what the card said. If the card's save isn't
			// the one last synced, ask rather than overwrite it.
			if g.path != "" && !s.cardUnchanged(g) {
				res.Conflicts = append(res.Conflicts, s.conflict(g, saveID, serverTime, "Both the card and RomM have a save, and the card's isn't the one last synced"))
				continue
			}
			err = s.download(g, saveID, romm.ParseTime(serverTime))
			if err == nil {
				res.Downloaded = append(res.Downloaded, g.title)
			}
		case "conflict":
			res.Conflicts = append(res.Conflicts, s.conflict(g, saveID, serverTime, "Both the card and RomM changed this save since the last sync"))
			continue
		case "delete":
			if g.path != "" {
				res.Conflicts = append(res.Conflicts, s.conflict(g, 0, "", "This save was deleted in RomM"))
			}
			continue
		default:
			continue
		}
		if err != nil {
			failed++
			res.Problems = append(res.Problems, g.stem+": "+err.Error())
		} else {
			done++
		}
	}
	progress(len(todo), len(todo))
	if s.session != 0 {
		_ = s.client.CompleteSession(s.session, done, failed)
	}
	return nil
}

// ResolveSave settles a conflict: keep "card" uploads the card's save over
// RomM's; keep "romm" puts RomM's save (saveID) on the card, or, when RomM's
// was deleted (saveID 0), takes the card's save off the card. The card's
// save is backed up on the PC before it's replaced or removed.
func (c *Core) ResolveSave(platform, stem string, romID, saveID int, keep string) error {
	p := platforms.ByKey(platform)
	if p == nil || sdcard.SafeFileName(stem) != stem {
		return fmt.Errorf("%q isn't a game on the card", stem)
	}
	s, err := c.newSyncer()
	if err != nil {
		return err
	}
	if s.server.DeviceID == "" {
		return errors.New("sync the saves first")
	}
	g := &saveGame{p: p, stem: stem, romID: romID}
	if err := g.readSave(s.root); err != nil {
		return err
	}
	switch {
	case keep == "card":
		if g.path == "" {
			return fmt.Errorf("%s has no save on the card any more", stem)
		}
		err = s.upload(g, true)
	case keep == "romm" && saveID != 0:
		latest := time.Time{}
		if sv, lerr := s.client.LatestSave(romID); lerr == nil && sv != nil && sv.ID == saveID {
			latest = romm.ParseTime(sv.UpdatedAt)
		}
		err = s.download(g, saveID, latest)
	case keep == "romm":
		if g.path != "" {
			if err = backupSave(g); err == nil {
				err = os.Remove(g.path)
			}
		}
		delete(s.server.Saves, g.key())
	default:
		return fmt.Errorf("can't keep %q", keep)
	}
	if err != nil {
		return err
	}
	return s.save()
}

// ---- backups -----------------------------------------------------------------

// backupsKept is how many old copies of each game's save are kept on the PC.
const backupsKept = 10

// SaveBackupDir is where card saves are copied before they're replaced:
// %APPDATA%\SlotDrop\Save backups.
func SaveBackupDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SlotDrop", "Save backups"), nil
}

// backupSave copies a card save to the PC, keeping the newest backupsKept.
func backupSave(g *saveGame) error {
	base, err := SaveBackupDir()
	if err != nil {
		return err
	}
	data := g.data
	if data == nil {
		if data, err = os.ReadFile(g.path); err != nil {
			return err
		}
	}
	dir := filepath.Join(base, g.p.Key, g.stem)
	name := g.stem + " " + time.Now().Format("2006-01-02 15-04-05") + filepath.Ext(g.path)
	if err := sdcard.WriteFileSafely(filepath.Join(dir, name), data); err != nil {
		return err
	}
	entries, _ := os.ReadDir(dir)
	var old []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".part") {
			old = append(old, e.Name())
		}
	}
	sort.Strings(old) // the date in the name sorts oldest first
	for len(old) > backupsKept {
		os.Remove(filepath.Join(dir, old[0]))
		old = old[1:]
	}
	return nil
}
