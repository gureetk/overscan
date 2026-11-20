<!-- SPDX-License-Identifier: Apache-2.0 -->

# Conformance suites

Black-box tests of what clients see: they speak to a server over HTTP, as clients do, and know nothing of how it is built. They are under Apache-2.0, like the [protocol notes](../docs/protocol/stalker-protocol-notes.md) they follow, so that other implementations and test suites can reuse them.

## `stalker/`

The Stalker portal API as Stalker clients use it, from the scenarios in the protocol notes (section 10). It builds Overscan from this repository, runs it on a free local port with a fictional lineup of three channels in two genres (`stalker/testdata/lineup.m3u`), streamed by a fake provider that has a guide for two of them and an archive of one, and a fictional library of sixteen films and a series of two seasons in a folder (`writeLibrary` in `stalker/vod_test.go`), and approves and blocks boxes with `overscan devices`, as an administrator would.

It covers so far:

| Scenario | Protocol notes |
| --- | --- |
| Signing in as a MAG: handshake, profile, `do_auth`, account information and its end date | Section 3 |
| A box that is unknown until approved, and a blocked box | Sections 2 and 3 |
| Calls without a token, or with one that a later handshake replaced | Section 3 |
| An action the portal lacks, answered with 404 | Section 3 |
| Where the API lives: `load.php` under the portal and at the root, and `/portal.php` | Section 1 |
| Genres, all channels, and paged channel lists by genre | Section 4 |
| Play-time links that play, and the reasons for no link: `nothing_to_play` and `limit` | Section 4 |
| The keep-alive call | Section 7 |
| The guide: a channel's programmes from now, every channel's over a period, and a day's table in the device's time zone | Section 5 |
| Catch-up: a channel's archive flags, the past programmes it keeps marked, and a past programme's link, which plays it from the archive; no link for one to come | Sections 4 and 5 |
| Films: categories, lists 14 to a page by name or by date, and a film's link, which plays the file from the part a player asks for | Section 6 |
| Series in the `series` module: a series' seasons with `movie_id`, each with its episodes' numbers, and an episode's link | Section 6 |

## `xtream/`

The Xtream API as players use it, from the [Xtream notes](../docs/protocol/xtream-api-notes.md), with Overscan's choices in their section 10. It builds Overscan and runs it with a fictional lineup of three channels in two groups (`xtream/testdata/lineup.m3u`), streamed by a fake provider with a guide for two of them and an archive of one, and a fictional library of sixteen films and a series of two seasons in a folder. Its setup alone is Overscan's own: it sets the administrator's password with `overscan admin password`, and signs in to the admin API to turn the outputs on and give the household's account a fictional login.

It covers so far:

| Scenario | Xtream notes |
| --- | --- |
| The account call, and a wrong login that does not sign in or play | Section 2 |
| Live categories, and every channel or one category's, with unique IDs, numbers and archive flags | Section 3 |
| A live channel's address, which plays MPEG-TS | Section 4 |
| The short guide, from the programme on now, with and without a limit, and the day's table with what is on now and in the archive | Section 5 |
| A past programme's timeshift address, in the server's time zone, which plays it from the archive; none for one to come | Section 6 |
| Film and series categories and lists, a film's and a series' details, and addresses that play a film or an episode from the part a player asks for | Section 7 |
| `get.php` with addresses that play, and `xmltv.php` with the channels of `epg_channel_id` | Sections 5 and 7 |
| An action the server lacks, answered with an empty list | Section 10 |

Run them with the rest of the tests, or alone:

    go test ./conformance/...

They need the `go` command, which builds the server. Fixtures are fictional; never put real portal or panel addresses, accounts or MAC addresses here ([clean-room policy](../docs/clean-room.md)).

