# Probe page

The probe page runs in a set-top box's browser and reports what that browser supports:

- its real user agent, screen and window size;
- which ES5 built-ins are missing, and which newer JavaScript features, syntax and DOM APIs exist;
- CSS support, storage, XHR, and which image formats it can show (PNG, JPEG, GIF, WebP, SVG);
- on a MAG box, which of the documented MAG objects exist, the members of `gSTB`, the device model, firmware, JS API version and video output mode;
- since version 2, the graphic resolution, whether the `URL` and `CustomEvent` constructors work, CSS grid, and which kinds of storage keep a value from one run to the next (see below).

Its results settled the MAG UI's toolchain ([plan](plan.md), section 9; D17 and D32 in the [decision log](decisions.md)). The page is hand-written ES5 and assumes nothing about the browser, so it also runs in a MAG emulator or a desktop browser, which is useful for comparison.

## Running it on the box

1. Run Overscan on a machine the box can reach, such as the LXC. Take the archive for that machine from the repository's Releases page (`linux_amd64` for 64-bit x86, `linux_arm64` for 64-bit ARM; `uname -m` tells you which), unpack it and start it with a writable data directory:

       tar xzf overscan_<version>_linux_amd64.tar.gz
       cd overscan_<version>_linux_amd64
       mkdir -p data
       OVERSCAN_DATA_DIR="$PWD/data" ./overscan

   Or build it from a checkout with `go build -o overscan ./cmd/overscan`. Overscan listens on port 8080, and reports are saved in the data directory's `probe/` folder.

2. On the box, open *System settings → Servers → Portals* and put `http://<server address>:8080/probe/` in *Portal 1 URL*, with any name in *Portal 1 name*. The **KB** button on the remote opens the on-screen keyboard. Save, then choose *System settings → Restart Portal*. To keep your current portal, use the second portal slot instead: with two portals set, the box asks which one to load, and pressing **Menu** while it shows *Loading portal...* also opens that choice. This follows Infomir's [External portal loading](https://wiki.infomir.eu/eng/set-top-box/stb-linux-webkit/embedded-portal/external-portal-loading) page; menu names can differ between firmware versions.

3. The page runs its tests for a few seconds, shows a summary, then says *Report saved on the server*.

4. The report is in `<data directory>/probe/`, one JSON file per run, named by the time it arrived. The server log has a `probe report saved` line with the file name.

5. Point the box back at its usual portal.

To compare, open the same address in StbEmu with a MAG322 profile, or in a desktop browser.

## Testing what survives a reboot

The MAG page has to keep a device secret somewhere, and the box has no `localStorage` (open question 7 in the [decision log](decisions.md)). Each run of the probe reads the markers the run before left, then leaves new ones naming itself, in:

- `stbStorage`, the box's own key-value storage;
- a cookie that the page sets, and an HttpOnly cookie that the server sets;
- a small file saved with `gSTB.SaveUserData`, in the box's user storage;
- `sessionStorage`, which should not survive, as a check.

To find out what survives a reboot:

1. Run the probe as above.
2. Restart the box completely: unplug it for a few seconds, then plug it back in.
3. Run the probe again.

The second run's summary shows what it found from the first, under *From the last run*; in its report, see `persistence.found` and `server.http_cookie_from_last_run`. Running the probe twice without the restart shows what survives a portal reload.

A box that requests the page twice when it loads a portal, as the MAG322w1 does, reports the server's cookie from its own first request, within the same second, so `server.http_cookie_from_last_run` cannot show what survived there.

## What a report contains

No device identifiers: the probe does not call the MAC address or serial number getters, and the server stores only what the page sends. A report does contain the user agent, model and firmware strings. They are not secret, but read a report before sharing it.

Probe results count as **Observed** under the [clean-room policy](clean-room.md). The docs record what a report shows, with the box model and firmware; raw reports stay out of the repository.

The server keeps at most 100 reports and refuses more until some are deleted.

## What it calls on a MAG box

Only objects and methods documented in Infomir's public [JavaScript API reference](https://wiki.infomir.eu/pub/doc/apinew/):

- It checks whether these objects exist: `gSTB`, `stbStorage`, `stbWebWindow`, `stbWildWebWindow`, `stbWindowMgr`, `stbEvent`, `stbUpdate`, `stbDownloadManager`, `pvrManager`, `dvbManager`, `timeShift`.
- It lists the members of `gSTB` and calls these getters: `GetDeviceModel`, `GetDeviceModelExt`, `GetDeviceVendor`, `GetDeviceVersionHardware`, `GetDeviceImageDesc`, `GetDeviceImageVersion`, `GetDeviceImageVersionCurrent`, `GetDeviceActiveBank`, `Version`.
- It reads the `tvsystem` and `graphicres` boot loader variables with `GetEnv`.
- It reads and writes the key `overscan-probe-mark` with `stbStorage.getItem` and `setItem`, and the file `overscan-probe-mark.txt` with `gSTB.LoadUserData` and `SaveUserData`. These markers, and the probe's cookies, hold only the time of the last run.
