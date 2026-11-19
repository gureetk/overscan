# The admin's web files

The admin's pages (D14, D44) are Go templates, rendered by `internal/admin` and embedded in the binary with the rest of `web/`. Nothing here needs building.

| Path | What |
| --- | --- |
| `templates/layout.html` | The layout of every page: the rail, or on a phone the bar along the top and the tabs along the bottom; the alerts, the logo mark and the icons. |
| `templates/*.html` | One file per page; each defines `content` for the layout. |
| `static/admin.css` | The styles: the TV interface's look (D52, D59), dark or light with the browser. |
| `static/admin.js` | Small helps that the pages work without: filtering the groups, ticking every channel, marking the channel open in the panel, closing menus, choosing a group on a phone. |
| `static/fonts/` | Instrument Sans, the same files as the MAG page's (`web/mag/src/assets/fonts`), under the SIL Open Font License 1.1 (`OFL.txt`). |
| `static/icon.svg` | The Overscan icon from `docs/logo`, as the favicon (Apache-2.0). |
| `static/vendor/` | Files from other projects, listed below. |

Static files are served at `/admin/static/` with a hash of their content in the address, so a browser keeps a file until it changes. The admin runs in desktop browsers, so its scripts need not be ES5, and `tools/check-es5.sh` leaves them out.

Pages carry a strict content security policy: scripts and styles come only from these files, never inline. htmx is configured to match, in `templates/layout.html`: no indicator styles of its own, no `eval`, no history kept in the browser's storage.

## Vendored files

| File | From | Licence |
| --- | --- | --- |
| `static/vendor/htmx-2.0.11.min.js` | [htmx](https://htmx.org) 2.0.11: `dist/htmx.min.js` of the npm package `htmx.org`, whose tarball has the integrity `sha512-Thx/WtpeOQqSrqBCw/A1cwGJGg4UrVa3+sW0GmrM3p4gJgO89ecH4qtbnyzDDWFvBTqjnIMCgELTNt636dtamA==` | 0BSD ([LICENSES/0BSD.txt](../../LICENSES/0BSD.txt)) |

Each vendored file starts with two comment lines, its SPDX licence identifier and where it came from; the rest of the file is the original, unchanged. To check htmx against the original, whose SHA-256 is `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`:

    tail -n +3 web/admin/static/vendor/htmx-2.0.11.min.js | sha256sum

To update it, fetch the new version with `npm pack htmx.org@VERSION`, check the tarball's integrity against `npm view htmx.org@VERSION dist.integrity`, replace the file under its new name with the two comment lines on top, and change the name in `templates/layout.html` and the version, integrity and checksum here.
