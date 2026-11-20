<!-- SPDX-License-Identifier: Apache-2.0 -->

# M3U and XMLTV output notes

What Overscan serves to players that read a playlist and a guide (D103): each account's M3U playlist of the channels it sees, and an XMLTV guide of them. How Overscan reads playlists and guides from its sources is in [playlist-notes.md](playlist-notes.md) and [catchup-notes.md](catchup-notes.md).

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a player confirms it). Nothing here is Observed yet: the matrix in [compatibility.md](../compatibility.md) will say what players did with it.

1. UniPlayer's public [M3U reference](https://uniplayer.net/docs/specs/m3u/) and [catch-up specification](https://uniplayer.net/docs/specs/catchup/).
2. The README of Kodi's [IPTV Simple Client](https://github.com/kodi-pvr/pvr.iptvsimple) (GPL-2.0-or-later). Only the documentation was used, never the add-on's code.
3. The XMLTV project's description of its format, [`xmltv.dtd`](https://github.com/XMLTV/xmltv/blob/master/xmltv.dtd).

## 1. Addresses

All of them are Overscan's own choice.

- `/playlist/<token>.m3u`, or `.m3u8`: the playlist.
- `/epg/<token>.xml.gz`: the guide, compressed with gzip and sent as `application/gzip`; `/epg/<token>.xml` sends it as it is.
- `/playlist/<token>/<entry>.ts`: a channel, as one continuous MPEG-TS stream. A source that sends HLS is relayed as one (D42), so every channel has the same kind of address.
- `/playlist/<token>/archive/<entry>.ts?start=S&end=E`: a past programme of a channel, from S to E in Unix seconds.
- `<token>` is the account's playlist token, 43 characters of URL-safe Base64. Making a new one stops every address made with the old one.
- `<entry>` is the channel's ID in the account's lineup, which stays the same across refreshes of its source.
- Every stream address plays through Overscan's stream hub, as a player of the account (D103): a player never sees a source's address or login.

## 2. The playlist

- The first line is `#EXTM3U url-tvg="<guide>" x-tvg-url="<guide>"`, naming the guide. Overscan reads either attribute from a source's playlist (plan, section 6.1). **Assumed:** a player reads one of them; a player that does not takes the guide's address on its own.
- Each channel has an `#EXTINF:-1` line, then its address. **Documented** [1, 2].
- `tvg-id` is the channel's ID in the guide (section 3), `tvg-name` its name, `tvg-chno` its number in the lineup, `tvg-logo` the address of its logo on Overscan (D54), and `group-title` its group. Overscan reads the same attributes from a source's playlist (plan, section 6.1). **Assumed:** players read them alike.
- A radio station has `radio="true"`. **Documented** [1, 2].
- A channel whose past programmes Overscan plays from its source's archive (D81, D82, D84) has `catchup="default"`, `catchup-source` with the archive address of section 1, `start={utc}&end={utcend}`, and `catchup-days`, the days back its archive reaches. **Documented** [1, 2]: `default` takes `catchup-source` as the whole address, and `{utc}` and `{utcend}` are the programme's start and end in Unix seconds, in UTC ([catchup-notes.md](catchup-notes.md), sections 2 to 4).
- The name after the comma is the channel's name. A `"` in an attribute becomes `'`, and line breaks become spaces. Overscan's choice.
- The channels are those the account's lineup shows, in its order. Locked channels are left out once the account has a parental PIN, since a player cannot ask for it, as for Stalker clients (D55). Overscan's choice.

## 3. The guide

- An XMLTV document [3]: `<tv>`, then a `<channel>` for each channel of the playlist, then its programmes. **Documented** [3].
- A channel's `id` is its `tvg-id` in the playlist: the lineup entry's ID, which no other channel on the server has, whatever the sources call their channels. Its `display-name` is its name, and its `icon` its logo's address. Overscan's choice.
- A programme has `start`, `stop` and `channel`, and `title`, `sub-title`, `desc` and `category` where the source's guide has them. Times are `YYYYMMDDhhmmss +0000`, in UTC, the source's own time zone having been taken into account when its guide was read (D51). **Documented** [3] for the form.
- The guide reaches from a day before now, or as far back as a channel's archive, to a week ahead. Overscan's choice.

## 4. What a player is told

All Overscan's own choice.

- While the outputs are off, every address answers 404, as if nothing were there; so does a token that no account has.
- A player outside the server's own networks is refused (403) unless the outputs answer anywhere. Its own networks are private addresses (RFC 1918 and RFC 4193), loopback, link-local, and the shared address space of RFC 6598, which some VPNs use. Behind a proxy, every player has the proxy's address.
- An account whose playlist is off, or that has ended (D95), is refused (403).
- A stream that its account's limit or its source's would exceed is refused (503), as the hub refuses a box's (D36, D102). A player that changes channel replaces its own stream; the hub knows a player by its account, its address and its user agent.
- Errors say what is wrong in a few words and never name a source, an address or a token.
