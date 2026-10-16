package romm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// SyncScopes are the extra API token permissions syncing saves needs: reading
// and writing saves (assets), and the card's place in RomM's device list.
const SyncScopes = "assets.read, assets.write, devices.read and devices.write"

// MaxSaveBytes caps a save file either way. Game Boy and GBA saves are at
// most 128 KB, so anything near this is not a save.
const MaxSaveBytes = 8 << 20

// SaveSlot is the RomM save slot SlotDrop syncs. RomM pairs saves on
// (game, slot); "autosave" is the name grout and other handheld clients use,
// so a save made on one of them carries on on Slot and back.
const SaveSlot = "autosave"

// SaveEmulator is what RomM is told made the save. Slot plays every system
// with mGBA (gpSP is optional for GBA), and both keep plain battery saves.
const SaveEmulator = "mGBA"

// StatusError is a RomM answer other than 2xx, kept so callers can tell a
// conflict (409) or a missing device (404) from other failures.
type StatusError struct {
	Code   int
	Detail string
	Msg    string
}

func (e *StatusError) Error() string { return e.Msg }

// IsStatus reports whether err is RomM answering with HTTP code.
func IsStatus(err error, code int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == code
}

// call sends one API request and returns the body of a 2xx answer.
func (c *Client) call(method, path string, params url.Values, body io.Reader, contentType string, limit int64) ([]byte, error) {
	raw := c.BaseURL + path
	if len(params) > 0 {
		raw += "?" + params.Encode()
	}
	req, err := http.NewRequest(method, raw, body)
	if err != nil {
		return nil, errorf("%q doesn't look like a web address.", c.BaseURL)
	}
	req.Header.Set("User-Agent", "SlotDrop/2.0")
	req.Header.Set("Accept", "application/json, */*")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	c.authorize(req)
	client := *c.http
	client.Timeout = 2 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, errorf("Couldn't reach %s: %v", c.BaseURL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errorf("The answer from %s was cut off: %v", c.BaseURL, err)
	}
	if resp.StatusCode < 300 {
		if int64(len(data)) > limit {
			return nil, errorf("RomM's answer to %s was too big.", path)
		}
		return data, nil
	}
	se := &StatusError{Code: resp.StatusCode, Detail: detail(data)}
	switch resp.StatusCode {
	case 401:
		se.Msg = "RomM refused the login. Check the username and password, or the API token."
	case 403:
		se.Msg = fmt.Sprintf("RomM won't let SlotDrop sync saves (HTTP 403). An API token needs the %s permissions, and a phone sign-in made before SlotDrop could sync needs doing again: sign out, then pair your phone again.", SyncScopes)
	default:
		se.Msg = fmt.Sprintf("RomM returned HTTP %d for %s", resp.StatusCode, path)
		if se.Detail != "" {
			se.Msg += ": " + se.Detail
		}
	}
	return nil, se
}

// detail is FastAPI's {"detail": "..."} message, if the body is one.
func detail(data []byte) string {
	var d struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(data, &d) != nil || d.Detail == nil {
		return ""
	}
	s, ok := d.Detail.(string)
	if !ok {
		b, _ := json.Marshal(d.Detail)
		s = string(b)
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func (c *Client) callJSON(method, path string, params url.Values, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, contentType = bytes.NewReader(b), "application/json"
	}
	data, err := c.call(method, path, params, body, contentType, MaxJSONBytes)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errorf("%s didn't answer like RomM does. Is the address right?", c.BaseURL+path)
	}
	return nil
}

// ---- types ----------------------------------------------------------------

// Save is a save file kept in RomM.
type Save struct {
	ID            int     `json:"id"`
	RomID         int     `json:"rom_id"`
	FileName      string  `json:"file_name"`
	FileSizeBytes int64   `json:"file_size_bytes"`
	UpdatedAt     string  `json:"updated_at"`
	Slot          *string `json:"slot"`
	Emulator      *string `json:"emulator"`
	ContentHash   *string `json:"content_hash"`
}

// ClientSave is one save on the card, as told to RomM when negotiating.
type ClientSave struct {
	RomID       int       `json:"rom_id"`
	FileName    string    `json:"file_name"`
	Slot        string    `json:"slot"`
	Emulator    string    `json:"emulator,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	Size        int64     `json:"file_size_bytes"`
}

// SyncOp is what RomM wants done with one save: "upload", "download",
// "conflict", "delete" (it was deleted in RomM) or "no_op".
type SyncOp struct {
	Action          string  `json:"action"`
	RomID           int     `json:"rom_id"`
	SaveID          *int    `json:"save_id"`
	FileName        string  `json:"file_name"`
	Slot            *string `json:"slot"`
	Reason          string  `json:"reason"`
	ServerUpdatedAt *string `json:"server_updated_at"`
	ServerHash      *string `json:"server_content_hash"`
}

// SlotName is the op's slot, or "" for RomM's archival (slotless) saves.
func (o *SyncOp) SlotName() string {
	if o.Slot == nil {
		return ""
	}
	return *o.Slot
}

// SyncPlan is RomM's answer to a negotiation.
type SyncPlan struct {
	SessionID  int      `json:"session_id"`
	Operations []SyncOp `json:"operations"`
}

// ---- calls ----------------------------------------------------------------

// RegisterDevice adds a device to RomM's device list and returns its id.
// SlotDrop registers each SD card, since the card is what holds the saves.
func (c *Client) RegisterDevice(name string) (string, error) {
	var out struct {
		DeviceID string `json:"device_id"`
	}
	err := c.callJSON("POST", "/api/devices", nil, map[string]any{
		"name":     name,
		"platform": "Slot",
		"client":   "SlotDrop",
		// Each card is its own device, so never let RomM hand back another
		// device that happens to look the same.
		"allow_duplicate": true,
	}, &out)
	if err != nil {
		return "", err
	}
	if out.DeviceID == "" {
		return "", errorf("RomM didn't give the SD card a device id.")
	}
	return out.DeviceID, nil
}

// Negotiate tells RomM which saves the card has and gets back what to do.
// romIDs are the games on the card (at most 500), so RomM only offers saves
// for games Slot can play.
func (c *Client) Negotiate(deviceID string, saves []ClientSave, romIDs []int) (*SyncPlan, error) {
	if saves == nil {
		saves = []ClientSave{}
	}
	var plan SyncPlan
	err := c.callJSON("POST", "/api/sync/negotiate", nil, map[string]any{
		"device_id": deviceID,
		"saves":     saves,
		"rom_ids":   romIDs,
	}, &plan)
	return &plan, err
}

// CompleteSession closes a sync session with how it went.
func (c *Client) CompleteSession(sessionID, done, failed int) error {
	return c.callJSON("POST", fmt.Sprintf("/api/sync/sessions/%d/complete", sessionID), nil,
		map[string]int{"operations_completed": done, "operations_failed": failed}, nil)
}

// UploadQuery says where an uploaded save goes.
type UploadQuery struct {
	RomID       int
	DeviceID    string
	ContentHash string
	SessionID   int
	// Overwrite makes RomM take the save even if the slot changed since the
	// card last synced. Only for when you chose the card's copy.
	Overwrite bool
}

// UploadSave sends a save to RomM's autosave slot. RomM answers 409 when
// the slot has a newer save than the card last saw.
func (c *Client) UploadSave(q UploadQuery, fileName string, data []byte) (*Save, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("saveFile", fileName)
	if err != nil {
		return nil, err
	}
	part.Write(data)
	if err := w.Close(); err != nil {
		return nil, err
	}
	params := url.Values{
		"rom_id":    {strconv.Itoa(q.RomID)},
		"slot":      {SaveSlot},
		"emulator":  {SaveEmulator},
		"device_id": {q.DeviceID},
		// Keep the last 10 versions, like grout does.
		"autocleanup":       {"true"},
		"autocleanup_limit": {"10"},
	}
	if q.ContentHash != "" {
		params.Set("content_hash", q.ContentHash)
	}
	if q.SessionID != 0 {
		params.Set("session_id", strconv.Itoa(q.SessionID))
	}
	if q.Overwrite {
		params.Set("overwrite", "true")
	}
	data, err = c.call("POST", "/api/saves", params, &buf, w.FormDataContentType(), MaxJSONBytes)
	if err != nil {
		return nil, err
	}
	var s Save
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, errorf("RomM's answer to the upload didn't look right.")
	}
	return &s, nil
}

// DownloadSave fetches a save's file. RomM isn't told the card has it until
// ConfirmDownloaded, once it's safely written.
func (c *Client) DownloadSave(saveID int, deviceID string, sessionID int) ([]byte, error) {
	params := url.Values{"device_id": {deviceID}, "optimistic": {"false"}}
	if sessionID != 0 {
		params.Set("session_id", strconv.Itoa(sessionID))
	}
	data, err := c.call("GET", fmt.Sprintf("/api/saves/%d/content", saveID), params, nil, "", MaxSaveBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errorf("RomM sent back an empty save.")
	}
	return data, nil
}

// ConfirmDownloaded tells RomM the card now holds a save, with the hash of
// what was written.
func (c *Client) ConfirmDownloaded(saveID int, deviceID, contentHash string) error {
	return c.callJSON("POST", fmt.Sprintf("/api/saves/%d/downloaded", saveID), nil,
		map[string]string{"device_id": deviceID, "content_hash": contentHash}, nil)
}

// LatestSave is the newest save in a game's autosave slot, or nil.
func (c *Client) LatestSave(romID int) (*Save, error) {
	var saves []Save
	err := c.callJSON("GET", "/api/saves", url.Values{"rom_id": {strconv.Itoa(romID)}, "slot": {SaveSlot}}, nil, &saves)
	if err != nil {
		return nil, err
	}
	var best *Save
	for i := range saves {
		s := &saves[i]
		if s.Slot == nil || *s.Slot != SaveSlot || s.RomID != romID {
			continue
		}
		if best == nil || ParseTime(s.UpdatedAt).After(ParseTime(best.UpdatedAt)) {
			best = s
		}
	}
	return best, nil
}

// ParseTime reads one of RomM's timestamps (zero if it can't). RomM leaves
// the zone off some of them; those are UTC.
func ParseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02 15:04:05.999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
