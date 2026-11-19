<!-- SPDX-License-Identifier: Apache-2.0 -->

# Conformance suites

Black-box tests of what clients see: they speak to a server over HTTP, as clients do, and know nothing of how it is built. They are under Apache-2.0, like the [protocol notes](../docs/protocol/stalker-protocol-notes.md) they follow, so that other implementations and test suites can reuse them.

## `stalker/`

The Stalker portal API as Stalker clients use it, from the scenarios in the protocol notes (section 10). It builds Overscan from this repository, runs it on a free local port with a fictional lineup of three channels in two genres (`stalker/testdata/lineup.m3u`), streamed by a fake provider that has a guide for two of them, and a fictional library of sixteen films and a series of two seasons in a folder (`writeLibrary` in `stalker/vod_test.go`), and approves and blocks boxes with `overscan devices`, as an administrator would.

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
| Films: categories, lists 14 to a page by name or by date, and a film's link, which plays the file from the part a player asks for | Section 6 |
| Series in the `series` module: a series' seasons with `movie_id`, each with its episodes' numbers, and an episode's link | Section 6 |

Run it with the rest of the tests, or alone:

    go test ./conformance/...

It needs the `go` command, which builds the server. Fixtures are fictional; never put real portal addresses, accounts or MAC addresses here ([clean-room policy](../docs/clean-room.md)).

Catch-up joins when Overscan implements it.
