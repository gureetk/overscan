# Players other than the boxes

Overscan can serve the household's channels to players other than the MAG boxes and Stalker clients: a player that reads an M3U playlist and an XMLTV guide, such as Kodi's IPTV Simple Client, VLC or TiviMate. Each person's player signs in as their account, sees that account's lineup, and plays through Overscan within the account's and each source's limits (D102, D103). The outputs are off until you turn them on, and so is each account's own switch: a household that turns on nothing sees nothing change.

What a player reads is described in [`protocol/m3u-output-notes.md`](protocol/m3u-output-notes.md). Which players have been tried, and what they did, goes in [`compatibility.md`](compatibility.md).

## Turning it on

1. Admin → *Accounts* → *Outputs for players* → *Serve players other than the boxes*.
2. On the account's card, *Change* → *Playlist and guide* → *Players read …'s playlist and guide*. The card shows two addresses: the playlist's, `http://<server>:8080/playlist/<token>.m3u`, and the guide's, `http://<server>:8080/epg/<token>.xml.gz`.
3. Give the player the playlist's address. Most players read the guide's address from the playlist; others take it on its own too.

A person who should have a lineup of their own, a limit of their own or their own end date gets an account of their own, from *Add an account* on the same page.

## What the player gets

- The channels that the account's lineup shows, in its order, with their numbers, groups and logos. Locked channels are left out once the account has a parental PIN, since a player cannot ask for it.
- Radio stations, marked as such.
- Past programmes of channels whose source keeps an archive, for players that play catch-up from a playlist.
- A guide of those channels, from a day back, or as far back as a channel's archive, to a week ahead, with every time in UTC.

Each channel plays through Overscan as one continuous MPEG-TS stream, never from the source's own address, which keeps its login on the server.

## Limits

- **The account's limit.** *Streams at once* on the account's card caps what its boxes, Stalker clients and players play together. A player that changes channel stops its own stream first; another player of the account is refused while the account is at its limit. Overscan tells players apart by their address and their user agent, so two players on one address with the same user agent count as one.
- **Each source's limit** holds as it does for the boxes: a source never plays more streams than it allows.

## Keep it on your network

The playlist's addresses need no password: whoever has them can play the account's channels. Treat them as a password, and make new ones from the account's card (*Make new addresses*) if they leak; the old ones stop working at once.

By default the outputs answer only players on the server's own networks: private addresses, the server itself, and VPNs that use the shared address space, such as Tailscale's. To reach them from outside, use a VPN, or a reverse proxy with TLS that you trust, rather than an open port; *Answer players anywhere* lifts the check, and should be on only behind one of those. Behind a proxy, every player seems to come from the proxy's address.
