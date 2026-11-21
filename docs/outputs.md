# Players other than the boxes

Overscan can serve the household's channels, films and series to players other than the MAG boxes and Stalker clients, in two ways:

- **A playlist and a guide**, for players that read an M3U playlist and an XMLTV guide, such as Kodi's IPTV Simple Client, VLC or TiviMate. Their addresses carry a token of the account's (D103).
- **An Xtream login**, for players that sign in to an Xtream server with a username and a password, such as TiviMate or OTT Navigator. They get the channels, with their guide and past programmes, and the films and series too (D104).

Each person's player signs in as their account, sees that account's lineup, and plays through Overscan within the account's and each source's limits (D102). The outputs are off until you turn them on, and so is each account's own switch: a household that turns on nothing sees nothing change.

What a player reads is described in [`protocol/m3u-output-notes.md`](protocol/m3u-output-notes.md) and in section 10 of [`protocol/xtream-api-notes.md`](protocol/xtream-api-notes.md). Which players have been tried, and what they did, goes in [`compatibility.md`](compatibility.md).

## Turning on a playlist and a guide

1. Admin → *Accounts* → *Outputs for players* → *Serve players other than the boxes*.
2. On the account's card, *Change* → *Playlist and guide* → *Players read …'s playlist and guide*. The card shows two addresses: the playlist's, `http://<server>:8080/playlist/<token>.m3u`, and the guide's, `http://<server>:8080/epg/<token>.xml.gz`.
3. Give the player the playlist's address. Most players read the guide's address from the playlist; others take it on its own too.

## Turning on an Xtream login

1. Admin → *Accounts* → *Outputs for players* → *Serve players other than the boxes*, as above.
2. On the account's card, *Change* → *Login*: a username, and a password of 8 to 128 letters without accents, digits, `.`, `_`, `-` or `~`, which is not the admin's or a source's → *Save the login*.
3. Turn on *Players sign in as … with its login*. The card shows the server's address, `http://<server>:8080`.
4. In the player, add an Xtream (or *Xtream Codes*) playlist with that address, the username and the password.

A player that reads only playlists can use the login too: `http://<server>:8080/get.php?username=<username>&password=<password>` is the account's playlist, and `http://<server>:8080/xmltv.php?username=<username>&password=<password>` its guide.

A person who should have a lineup of their own, a limit of their own or their own end date gets an account of their own, from *Add an account* on the same page.

## What the player gets

- The channels that the account's lineup shows, in its order, with their numbers, groups and logos. Locked channels are left out once the account has a parental PIN, since a player cannot ask for it.
- Radio stations, marked as such.
- Past programmes of channels whose source keeps an archive, for players that play catch-up from a playlist.
- A guide of those channels, from a day back, or as far back as a channel's archive, to a week ahead, with every time in UTC.
- With an Xtream login, the films and series too, as the boxes' *Movies* and *Series* show them, except a Stalker portal's, which Overscan reads a page at a time as a box browses them. A film's or a series' details are read from its source when a player opens it, as when a box does.

An account that uses only some sources, as its card's *Sources* says, gets only what comes from them: the channels with a stream of theirs, their own or a backup's, playing from those streams alone, and the films and series of those sources. Recordings play from the `recordings` folder source, so such an account plays them only if that source is among its own.

Each channel plays through Overscan as one continuous MPEG-TS stream, never from the source's own address, which keeps its login on the server. With an Xtream login, a player that asks for HLS gets it for channels whose source sends HLS. Films and episodes play as their files are, and the player can seek in them.

A player that cannot play what a source sends, or a network too slow for it, can get live channels made H.264 at up to 720p or 1080p, with AAC sound: choose it under *Live streams* on the account's card, and turn on *Transcoding* in *Settings*, whose number at a time each such stream counts against. Transcoding takes a strong processor; without it, or when the number at a time is reached, channels play as their sources send them.

## Limits

- **The account's limit.** *Streams at once* on the account's card caps what its boxes, Stalker clients and players play together. A player that changes channel stops its own stream first; another player of the account is refused while the account is at its limit. Overscan tells players apart by their address and their user agent, so two players on one address with the same user agent count as one.
- **Each source's limit** holds as it does for the boxes: a source never plays more streams than it allows.

## Keep it on your network

The playlist's addresses need no password: whoever has them can play the account's channels. Treat them as a password, and make new ones from the account's card (*Make new addresses*) if they leak: the old ones stop working at once, and so does what players play with them. An Xtream player sends the account's password in every address it asks for, so give the login a password of its own, and change it if it leaks: what players play with the old one stops too.

Turning off the outputs, an account's playlist or its Xtream login stops what their players play at once, and so does the account's end (D105).

By default the outputs answer only players on the server's own networks: private addresses, the server itself, and VPNs that use the shared address space, such as Tailscale's. To reach them from outside, use a VPN, or a reverse proxy with TLS that you trust, rather than an open port; *Answer players anywhere* lifts the check, and should be on only behind one of those. Name such a proxy in `trusted_proxies` in the configuration file (D119): the check then looks at the player that the proxy names in its `X-Forwarded-For` header, and refuses a request that names no one, and wrong logins count against that player alone. A proxy that is not named there seems to be every player: the check lets every player through, so the proxy has to decide who reaches the outputs, and wrong logins through it make every player's logins wait for a while, since they count by address.

Wrong logins slow down as the admin's sign-in does: after five from one address within 15 minutes, that address's logins wait until the first of them is 15 minutes old, and after 50 from anywhere, everyone's do. What plays goes on, and players that use a playlist's addresses are not affected. [`security.md`](security.md) says what else Overscan does to keep the outputs safe.
