<!-- SPDX-License-Identifier: Apache-2.0 -->

# Catch-up notes

How sources describe their archives of past programmes, which players call catch-up, archive, timeshift or DVR, and the addresses that play them. Overscan reads these from M4: an Xtream server's archive (D81), and a playlist's (D82). A Stalker portal's archive comes through its own API ([stalker-protocol-notes.md](stalker-protocol-notes.md), section 5).

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real source confirms it). Nothing here is Observed yet.

1. UniPlayer's public [catch-up specification](https://uniplayer.net/docs/specs/catchup/) and [M3U reference](https://uniplayer.net/docs/specs/m3u/).
2. The README of Kodi's [IPTV Simple Client](https://github.com/kodi-pvr/pvr.iptvsimple) (GPL-2.0-or-later), its *Catchup* section and its list of format specifiers, and the add-on's announcement thread on the [Kodi forum](https://forum.kodi.tv/showthread.php?tid=351431). Only the documentation was used, never the add-on's code.
3. Flussonic's documentation of [DVR playback](https://flussonic.com/doc/dvr-playback) and of [play addresses](https://flussonic.com/doc/play/).
4. The [Xtream API notes](xtream-api-notes.md), section 6, and their sources.

## 1. Where a playlist describes its archive

- Attributes on a channel's `#EXTINF` line say how it plays its archive. **Documented** [1, 2].
- The same attributes on the `#EXTM3U` line are defaults for every channel, and a channel's own attribute of the same name wins. **Documented** [1, 2].
- The archive is chosen from the guide: a player plays a programme that its guide lists, from the programme's start. **Documented** [1].

## 2. Attributes

- `catchup`: the mode (section 3). `catchup-type` is another name for it. **Documented** [1].
- `catchup-source`: the archive's address with placeholders (section 4), or what goes after the live address, by the mode. **Documented** [1, 2].
- `catchup-days`: how many days back the archive reaches. **Documented** [1, 2].
- `catchup-correction`: a correction in hours, applied to the times put in the address, for an archive whose clock is not the guide's; [2] bounds it from −12 to +14 hours. **Documented** [1, 2]. Neither source gives the sign; **Assumed:** a positive correction asks for a later time.
- Older attributes: `timeshift`, whose presence means the shift mode and whose value is the days; `tvg-rec`, days; `catchup-time`, the archive's reach in seconds. **Documented** [1]. [1] reads the days from `catchup-days`, then `timeshift`, then `tvg-rec`, then `catchup-time`. **Documented** [1].

## 3. Modes

- `default`: `catchup-source` is the archive's whole address. **Documented** [1, 2].
- `append`: `catchup-source` goes after the live address, as a query that starts with `?`, or `&` when the live address has one. **Documented** [1, 2].
- `shift` (also `timeshift`): `?utc=<start>&lutc=<now>` goes after the live address, with `&` in place of `?` when it has a query already, in Unix seconds. **Documented** [1, 2].
- `flussonic`, `flussonic-hls`, `flussonic-ts` and `fs`: the live address of a Flussonic server, made into an address of its archive (section 5). **Documented** [1].
- `xc` (also `xtream`, `xstream` in [1]): the live address of an Xtream server, made into its timeshift address (section 6). **Documented** [1].
- `vod`: the archive's programmes are titles named by the guide's `catchup-id`. **Documented** [1].

## 4. Placeholders

Times in Unix seconds are in UTC. **Documented** [1, 2].

- `{utc}` and `${start}`: the programme's start.
- `{utcend}` and `${end}`: its start and its length.
- `{lutc}`, `${now}` and `${timestamp}`: now.
- `{duration}`: its length in seconds; `{duration:X}`, that divided by X.
- `{offset:X}`: now less its start, in seconds, divided by X.
- `{Y}`, `{m}`, `{d}`, `{H}`, `{M}` and `{S}`: the year, month, day, hour, minute and second of its start. **Documented** [1, 2]; neither source says in which time zone.
- A time placeholder can take a format after a colon, made of the letters `Y`, `m`, `d`, `H`, `M` and `S`, such as `{utc:Y-m-dTH:M:S}`. **Documented** [1, 2].
- `{catchup-id}`: the guide's own ID of the programme. **Documented** [1].
- `${duration}` and `${offset}` are named in [1] and [2] with some modes. **Documented**.

## 5. Flussonic

- An HLS address, `.../STREAM/index.m3u8`, plays the archive between two times as `.../STREAM/index-<start>-<seconds>.m3u8`, a playlist that ends. **Documented** [3]. [1] uses the same forms for `mono.m3u8` and `video.m3u8`. **Documented** [1].
- An MPEG-TS address, `.../STREAM/mpegts`, plays from a time on as `.../STREAM/timeshift_abs-<start>.ts`, a stream that runs on to now. **Documented** [1, 3].

## 6. Xtream servers

- A channel with `tv_archive` set keeps `tv_archive_duration` days, and plays at `/timeshift/U/P/<minutes>/<YYYY-MM-DD:HH-MM>/<stream_id>.ts`, or `.m3u8`. **Documented** [1, 4].
- [1] says the start is in the server's archive time zone; the Xtream notes' sources disagree (section 6 there).

## 7. What Overscan does

These are Overscan's choices where the sources leave room (D81, D82). They are not observations of real sources.

- **Which channels:** a playlist's channel has an archive that Overscan plays when it names a mode that Overscan plays (`default`, `append`, `shift`, a Flussonic mode or an Xtream mode) and, for `default` and `append`, a catch-up source. Without a mode, a `timeshift` attribute means `shift`, a catch-up source that starts with `?` or `&` means `append`, another means `default`, and a live address in the Xtream form with days means `xc` (**Assumed**). `vod`, and a source that needs `{catchup-id}`, are not played: Overscan keeps no guide IDs.
- **Days:** as [1] reads them; a channel that names a mode but no days keeps one; at most 60.
- **Time zone:** each source has a setting for the zone of the dates and times in its addresses: an Xtream server's, or else UTC, until the administrator names another. Unix seconds are always UTC.
- **Whole minutes:** a stream from a time starts at the whole minute at or before it, so that every kind of address starts where the page counts from. An Xtream address asks for the minutes that are left of the programme, rounded up; a Flussonic HLS address, for the seconds.
- **Placeholders** that are not listed above are left as they are. A catch-up source starting with `?`, in `append` mode, gets `&` in its place when the live address has a query already.
- **Ends:** a stream that runs on past the programme, as `shift` and Flussonic's MPEG-TS do, is ended by the page two minutes after the programme's end.
- **Secrets:** a catch-up source can hold the provider's login, so it is kept sealed, as stream addresses are (D46), and no log or error quotes an address.
