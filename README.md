# SlotDrop

**Pick it in RomM. Play it on Slot. Label included.**

SlotDrop puts games from your [RomM](https://github.com/rommapp/romm) library
onto a [Slot](https://slot-cfw.fyi) SD card, each with its cartridge label and
shell colour. One `SlotDrop.exe`, nothing to install.

> [!IMPORTANT]
> **Please read this first.**
>
> I'm not a developer by any means. My day job is infrastructure as code, and
> that's about the extent of my coding: I can read and understand code, but I
> can't write an app like this from scratch. SlotDrop started as an idea in my
> head. When I showed a proof of concept to Slot's lead developer, he gave me
> his blessing to go ahead and make it.
>
> It was built with **Claude by Anthropic**. I steered it with a
> security-first approach (warnings about insecure connections, up-to-date
> patched dependencies, a security review before release) and Claude did the
> rest of the building.
>
> It has been tested against a **RomM 5.4** server. I just wanted something
> simple: instead of downloading a game, unzipping it and dropping it onto the
> card by hand, SlotDrop does it all for you, backed by
> [RomM](https://github.com/rommapp/romm), your powerful, beautiful ROM
> management system. It can also sync your saves with RomM, both ways.
>
> Use it at your own risk, and keep a backup of your SD card.

![SlotDrop showing the games on an SD card, each drawn as its cartridge](docs/screenshots/your-card.png)

## What it does

- Browse and search the GBA, Game Boy and Game Boy Color games in your RomM library.
- Click **+** and the game is downloaded (unpacking .zip, .7z or .rar) into
  `Games\<GBA|GB|GBC>` on the card.
- Its label is cut from RomM's cartridge photo and saved to
  `Labels\<system>\<game>.png`, and a shell colour is written to
  `Labels\cart_shell.txt`.
- Pick a shell colour and a Solid, Clear or Glitter finish, redraw the label
  crop, or use your own image.
- **Sync saves** with RomM, both ways: new saves on the card go up to RomM,
  saves made elsewhere (grout on a handheld, say) come down to the card.
- Remove a game from the card (your saves stay) and eject the card safely.

| Pick a game | Tick several and add them at once |
|---|---|
| ![A game's panel with its label, shell colour and finish](docs/screenshots/game-panel.png) | ![Three games ticked, with an Add 3 games bar](docs/screenshots/pick-several.png) |

![Pokémon Heart & Soul on a Slot, in the blue shell SlotDrop gave it](docs/screenshots/on-slot.jpg)

## Prerequisites

- Windows 10 or 11 (the window uses WebView2, which both already have).
- A RomM server you can sign in to, with a password, an API token, or your
  phone (RomM 5.3 or newer).
  An API token needs **platforms.read** and **roms.read** for the library, and
  **assets.read**, **assets.write**, **devices.read** and **devices.write** to
  sync saves. Give it an expiry rather than *Never*; a year is plenty.
- Labels are cut automatically from RomM’s `physical` cartridge photos. RomM doesn’t download these by default:
  turn on *physical* in its scan media settings, re-scan your library, then restart SlotDrop.
  Games without one still install, and you can pick your own label image.
- An SD card set up for Slot.

## Using it

1. Download the zip from [Releases](../../releases), unzip it and run
   `SlotDrop.exe`. The app isn't code-signed, so Windows SmartScreen will
   probably say *Windows protected your PC*: click **More info**, then
   **Run anyway**. Each release lists its SHA-256 and a VirusTotal scan.
2. If your Slot card is plugged in it's picked up automatically. Otherwise
   click it in the list, or use *Choose folder…*.
3. Sign in to RomM with your password or an API token, or pick *Phone (QR)*,
   scan the code and approve SlotDrop in RomM on your phone. Leave
   *Remember me* ticked and it signs you in by itself next time.

   <img src="docs/screenshots/phone-sign-in.png" width="480" alt="Signing in with your phone: a QR code to scan, and a code to type instead">

4. Search at the top, then click **+** on a game. To add several, tick the
   circle on each game and press *Add N games* in the bar at the bottom.

Press **Sync saves** in the top bar to sync the card's saves with RomM. If a
save changed on both sides, or SlotDrop can't tell which copy is newer,
nothing is written: it shows you both and you pick the one to keep.

Click any game to see its cartridge. For games on the card you can pick a shell
colour (**GBA Grey** is what new games get; Slot draws a game with no colour in
plain black), choose a finish, redo the label by dragging a box on RomM's
cartridge photo, or *Use my own image…*. If games on the card have no label,
*Make labels* does them all. When you're done, the eject button next to the
card in the top bar makes it safe to pull out.

## Building it yourself

You need [Go](https://go.dev/dl/) 1.26 or newer. Then double-click `build.bat`,
or run:

```
go build -trimpath -tags desktop,production -ldflags "-H windowsgui -s -w" -o SlotDrop.exe .
```

From Linux or macOS, put `GOOS=windows GOARCH=amd64` in front.

Run the tests with `go test ./...`.

The app icon and Windows manifest live in `build/winres` and are compiled into
`rsrc_windows_amd64.syso` with [go-winres](https://github.com/tc-hib/go-winres):
`go-winres make --in build/winres/winres.json --arch amd64`.

## Save sync

- Slot keeps a game's save as `Saves\<GBA|GB|GBC>\<game>.sav`. SlotDrop syncs
  it with RomM's **autosave** slot, the one grout and other handheld apps use,
  for every game on the card that RomM knows. Other slots and saves you
  uploaded by hand in RomM are left alone, and so are save states.
- Each SD card is its own device in RomM's device list (*Slot SD card*), so
  RomM knows what that card last synced, whichever PC syncs it. SlotDrop keeps
  the card's device id and what it last synced in `.slotdrop\sync.json` on the
  card.
- Before a save on the card is replaced or removed, it's copied to
  `%APPDATA%\SlotDrop\Save backups`, keeping the last 10 of each game.
- Slot's clock can be wrong. A save that changed since the last sync is never
  taken for an old one, and a card that has never synced never has its saves
  overwritten without asking.

## Security notes

- Your RomM password or token is only kept if you tick *Remember me*, and then
  only encrypted with Windows DPAPI, so only your Windows account on that PC
  can unlock it. It lives in `%APPDATA%\SlotDrop\settings.json`. *Sign out* in
  the RomM menu forgets it.
- Your login is only ever sent to your RomM server and never written to a log.
- Signing in with your phone gets a token with only the permissions SlotDrop
  needs: reading the library, and reading and writing saves and devices for
  save sync. The QR code only ever points at your own RomM address, and you
  can remove SlotDrop from RomM's device list at any time. If you paired before
  SlotDrop could sync saves, sign out and pair again to sync.
  If your server address is HTTPS, the login is never sent over plain HTTP,
  even after a redirect. Using an `http://` address shows a warning.
- HTTPS certificates are checked against the Windows certificate store, like
  your browser.
- Game and file names from RomM are shown as plain text, never run as code.
  The page has a strict Content Security Policy and loads nothing from the web.
- Downloads have size limits, zips are read in memory, file names are cleaned
  for FAT32/exFAT, and games are written to a `.part` file first so a failed
  copy never looks like a game.

Found a security problem? Please open an issue saying so, without the details,
and I'll get in touch.

## The files

| Where | What |
| --- | --- |
| `main.go`, `app.go` | the window, and what the page can ask the app to do |
| `frontend/` | the page: `index.html`, `style.css`, `app.js`, `icons.js` |
| `internal/romm` | talks to RomM's API |
| `internal/sdcard` | Slot's folders and `cart_shell.txt` (reads the old `.ini` too) |
| `internal/label` | finds the label on a cartridge photo and cuts it out |
| `internal/match` | pairs card games with RomM games (name, then checksum) |
| `internal/core` | ties it together: adding games, making labels, syncing saves |
| `internal/secret`, `internal/settings` | the encrypted saved login |
| `build/winres`, `rsrc_windows_amd64.syso` | the app icon and Windows manifest |
| `preview.go` | development only: runs the page in a browser with a fake RomM (`go run -tags preview . <card folder>`) |

## Licence

MIT, see [LICENSE](LICENSE).

SlotDrop is an unofficial companion app and isn't affiliated with RomM or
Slot. You're responsible for owning the games you put on your card.
