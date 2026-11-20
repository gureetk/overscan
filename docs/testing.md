# Testing

How Overscan is tested, and how to run each kind of test. The [plan](plan.md), section 15, sets out the approach.

## On every pull request

CI checks formatting, module tidiness and vet, runs the Go tests with the race detector, the conformance suite and the fuzz tests' seeds among them, and checks licence headers, ES5 syntax in browser scripts, sign-offs and known vulnerabilities. It installs ffmpeg first, so that the stream repair tests run the real one. For the MAG page it type-checks the sources, builds the page, fails if the build differs from the committed `web/mag/dist/`, and lints the build. It also builds the Docker image from a release archive's files and checks it with `tools/check-image.sh`. [CONTRIBUTING](../CONTRIBUTING.md) lists the commands to run the same checks locally.

## Fuzzing

The parsers of what sources send have fuzz tests: M3U playlists, XMLTV guides, HLS playlists, the stream analyser and the repeat filter. Their seeds run with every test run; to fuzz one for a while:

    go test ./internal/hls -run '^$' -fuzz '^FuzzParse$' -fuzztime 1m

Inputs that fail are saved under the package's `testdata/fuzz/` and stay there as seeds once fixed.

## Conformance

Black-box tests of what Stalker clients see, over HTTP, against a running Overscan: [`conformance/README.md`](../conformance/README.md). They run with the other tests:

    go test ./conformance/...

## Soak test

`tools/soak` runs Overscan under load and records its memory and CPU use:

1. It serves a fake provider with a lineup of 20,000 channels in 200 groups, each an endless MPEG-TS stream at 5 Mbit/s.
2. It runs Overscan against that lineup, built from the repository unless `-bin` names a binary, and lets it idle for 30 seconds with the lineup loaded.
3. It simulates four boxes for ten minutes, half through the Stalker API and half as the MAG page through the device API. Each signs in, loads the whole channel list, then watches random channels, switching every two minutes on average; the Stalker boxes also make the keep-alive call.
4. Every five seconds it reads the server's resident memory and CPU time from `/proc`, so it runs on Linux only.

Run it from the repository:

    go run ./tools/soak
    go run ./tools/soak -duration 24h -boxes 8

| Flag | Default | Meaning |
| --- | --- | --- |
| `-duration` | `10m` | How long the boxes run |
| `-boxes` | `4` | How many boxes, each watching one stream |
| `-channels` | `20000` | How many channels the fake provider lists |
| `-bitrate` | `5000000` | Each stream's rate, in bits per second |
| `-zap` | `2m` | How long a box stays on a channel, on average |
| `-bin` | | The Overscan binary to test, instead of building one |
| `-out` | `soak.csv` | Where the samples go |
| `-idle-mb`, `-peak-mb` | `60`, `150` | Memory budgets, in MB; 0 for none |
| `-cpu` | `0` | A budget for average CPU use under load, in percent of one core; 0 for none |

It prints a summary and writes the samples, and it fails if the server breaks a budget or if any stream or call fails. The memory budgets are the plan's: under about 60 MB idle, and under about 150 MB with a large catalog.

The *Soak* workflow runs the ten-minute version every Monday, and on demand from the repository's Actions tab, and keeps the samples for 30 days. Hosted runners vary from run to run, so the 24-hour run belongs on the LXC.

### Baseline

The first ten-minute run, in November 2026, with the defaults, against the server as released in v0.0.4, on a cloud machine with two cores of an Intel Xeon at 2.1 GHz, not the LXC.

| Measure | Result | Budget |
| --- | --- | --- |
| Resident memory, idle with the 20,000-channel lineup loaded | 23.8 MB | 60 MB |
| Resident memory at the peak | 63.9 MB | 150 MB |
| Resident memory at the end | 30.3 MB | |
| CPU, average under load: four streams, 20 Mbit/s in all | 1.0% of one core | A few percent |
| CPU at the peak, over five seconds | 8.6% of one core | |
| Streams | 23 played, none failed, 1.5 GB proxied | |

Memory peaks in the first seconds, when the four boxes load the whole lineup at once: each answer is built in memory before it is sent, and the Stalker one runs to several megabytes for 20,000 channels. It falls back over the following minutes as Go returns memory to the system. Paged lists for the MAG page, and answers written as they are built, would lower the peak.

## Upgrades

From v0.1.0 on, each release is tried as an upgrade of the one before. The previous release reads a source and gets an approved device. The new release then starts on the same data directory, and a Stalker client must see the same channels, with the same numbers, and play one. For v0.1.0 this was done from v0.0.9 and from v0.0.17, directly and one after the other, with a fictional playlist of 3,000 channels. For v0.4.0's pre-releases, each was tried from the one before, and the last from v0.3.0 directly.

## On the box

A checklist for the MAG322w1, to go through with each release that changes the page or playback. [Trying Overscan](trying-it.md) says how to set the box up.

- The page loads from a portal address ending in `/c/` and from one ending in `/stalker_portal/c/`, and the server log says so once per load.
- A new box shows the command that approves it, with its pairing code; once it is approved, Home appears by itself.
- The page's own typeface shows, with figures that line up in the clock and the lists.
- Home's buttons open Live TV, the guide, search and settings; Movies and Series say they come later. *With a Picture* shows the picture, the last channel and the favourites.
- In Live TV, the arrows, Page Up and Down, and the channel keys move through groups and channels, and the list answers a key within about 150 ms. Logos show, and what is on now, with its progress.
- OK plays a channel in the small player within a few seconds, with picture and sound, exactly inside the frame on the right, for MPEG-TS and HLS channels alike; the guide's small window likewise. OK again goes full screen.
- Every icon shows, on Home in both looks, in Settings and in the key hints.
- A channel served as HLS from an address without `.m3u8` plays on past its first half minute, and the server log says that the hub relays it.
- Full screen, the channel keys change channels, digits go to a channel by number, and the banner shows what is on and the picture's resolution. Yellow lists the sound tracks of a channel with several, and choosing one changes the sound.
- Red adds a channel to the favourites and takes it out, in Live TV, the guide and the banner, and the account's other boxes see the change.
- The guide shows two hours of programmes, moves earlier and later with Left and Right and a page at a time with the channel keys, and plays what is on with OK.
- Search finds channels by name and number, and programmes, with the on-screen keyboard.
- A locked channel asks for the PIN; a wrong PIN says so, and the right one plays it.
- Changing the appearance changes the page at once, and survives *Restart Portal*.
- With *Channel Numbers* set to *Each Group from 1*, a group's channels count from 1, and typing a number in the group plays that channel of the group. All Channels keeps the lineup's numbers.
- Moving through the groups in Live TV keeps up with the remote; the channels follow once the focus rests.
- With *Live TV Groups* set to *Open with OK*, moving through the groups leaves the channels as they are and the footer says *OK Open*; OK or Right opens the group, and Back returns to the group already open. The setting survives *Restart Portal*.
- The volume and mute keys work.
- Back from full screen returns to Live TV, on the channel that plays, and Back from Live TV stops it.
- A channel whose source is at its connection limit says so.
- A channel set to be repaired plays with picture and sound, in the small player and full screen, a second or two later than before; the *Overview* marks it *Repaired*, and its sound tracks can still be chosen. With *Repair every channel* on, any channel is marked *Repaired* but those set to *Don't repair*. A channel that stuttered, skipped or jumped back plays smoothly, or its `debug channel` report with `-repair on` shows a clock without jumps.
- With *Repair HLS streams* on, a repaired channel whose address ends in `.m3u8` plays, the log says `relaying an HLS stream as one continuous stream` and `repairing a stream with ffmpeg`, and the *Overview* marks it *Repaired*. With it off, the same channel plays as HLS and is not marked.
- A change in the admin's lineup reaches the box within five minutes, without *Restart Portal*.
- After the server restarts, the page carries on at its next call.
- After *Restart Portal*, and after a full restart of the box, the page comes back without another approval.

For a household (M5):

- With two profiles, Home names the profile watching, the yellow key switches to another, asking for its PIN if it has one, and each profile keeps its own favourites and resume points.
- Past the household's end date, the box says that the account has ended, with the date, and plays nothing; with the date removed, it comes back by itself.
- A message from the admin shows over whatever is on screen until OK, and does not come back.
- A theme chosen for the household, and another for a profile, change the box's look within five minutes, with every screen still readable; *Overscan* brings back the page's own look.
- With the shared tuner on, a box and a recording of the channel it plays use one connection to the source.

## The admin with the box

A checklist for releases that change the admin, the lineup or sources, with the box set up as above.

- The admin's *Devices* page approves a new box by the code it shows, and the box lists the channels a few seconds later.
- A group hidden, a channel renamed, a number set and a channel moved to another group on the *Live TV* page show on the box after *Restart Portal*; numbers that were not touched stay as they were.
- After the source is read again (*Read now* on the *Sources* page), every lineup edit is still there.
- A source's *Test* reports its channels without changing the lineup, and a source turned off takes its channels off the box after *Restart Portal*.
- While the box plays a channel, the *Overview* lists it with the box's label, and the entry goes away about a minute after playback stops.
- No page and no log line shows a source's address or login; the *Sources* page shows only its host.
- The support bundle, on the *Settings* page, downloads as a zip file, and none of its files holds a source's host or login, an IP address, or more than the first half of a MAC address.
- An Xtream source's *Test* names the account's end date and limit of streams, and its card shows them after a read. A box plays its channels, and the *Overview* lists them.
- A playlist source changed to an Xtream source of the same server keeps its channels, numbers and edits: the log's `source refreshed` line says `added=0`.
- After a source's guide is read (`guide read` in the log, and the source's card), a Stalker client shows what is on now and next on a channel with a guide, and its day guide shows today's programmes at the right times for the device's time zone.
- A channel without a guide ID of its own, such as another quality of a channel that has one, shows that channel's guide.
- An upgrade from the previous release keeps the lineup as the box shows it, with its edits.
- A Stalker client given the box's MAC address is refused, and the server log says why.
- Blocking the box shows that it is blocked at its next call, and approving it again lets it back in.
- On a box that offers it, the same at 1920×1080 graphics resolution. The MAG322w1 offers at most 1280×720.
