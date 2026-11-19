<!-- SPDX-License-Identifier: Apache-2.0 -->

# Playlist notes

What Overscan reads from an M3U playlist's channels beyond their names, groups, logos and addresses. How a playlist describes its archive of past programmes has notes of its own, [catchup-notes.md](catchup-notes.md).

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real source confirms it). Nothing here is Observed yet.

1. UniPlayer's public [M3U reference](https://uniplayer.net/docs/specs/m3u/).
2. The README of Kodi's [IPTV Simple Client](https://github.com/kodi-pvr/pvr.iptvsimple) (GPL-2.0-or-later), its section on the M3U format. Only the documentation was used, never the add-on's code.

## 1. Radio stations

- `radio="true"` on a channel's `#EXTINF` line marks a radio station, a stream with sound and no picture. **Documented** [1, 2].
- The value is `true` in any case; any other value, or none, is a channel. **Documented** [2].
- [2] lists the attributes of the `#EXTM3U` line, and `radio` is not among them, so Overscan reads it on a channel's line alone. **Documented** [2].
- Overscan keeps the mark, and the box plays such a channel on its *Radio* screen (D87).
