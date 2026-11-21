# Testing

How Overscan is tested, and how to run each kind of test. The [plan](plan.md), section 15, sets out the approach.

## On every pull request

CI checks formatting, module tidiness and vet, runs the Go tests with the race detector, the conformance suite and the fuzz tests' seeds among them, and checks licence headers, ES5 syntax in browser scripts, sign-offs and known vulnerabilities. It installs ffmpeg first, so that the stream repair tests run the real one. For the MAG page it type-checks the sources, builds the page, fails if the build differs from the committed `web/mag/dist/`, and lints the build. It also builds the Docker image from a release archive's files and checks it with `tools/check-image.sh`; builds the release's archives and Debian packages and, with `tools/check-packages.sh`, installs, upgrades and removes each on a fresh Debian system running systemd; and upgrades a household from every full release since v0.3.0 to the change, as [*Upgrades*](#upgrades) says. [CONTRIBUTING](../CONTRIBUTING.md) lists the commands to run the same checks locally.

## Fuzzing

Every parser of what comes from outside has a fuzz test, which checks that it never fails on what it is given and that what it keeps makes sense:

- what sources send: M3U playlists and their catch-up attributes, XMLTV guides, HLS playlists, MPEG-TS streams (their packets and tables, the stream analyser, the repeat filter and timeshift's places to start), Xtream servers' answers (the account, categories, live streams, films and series), Stalker portals' answers (genres, channels, links, pages of films and series, guides and dates), Jellyfin's and Plex's items, the names and `.nfo` files in folders, subtitles, ffprobe's reports, and the text of a source's answers that goes into the log;
- what devices and players send: MAC addresses, the references of what Stalker clients play, and the names of streams in the outputs' addresses;
- what the administrator gives: lineup files, theme files and rules' patterns;
- and what Overscan makes from all of it: the names of recordings' files.

Their seeds run with every test run; to fuzz one for a while:

    go test ./internal/hls -run '^$' -fuzz '^FuzzParse$' -fuzztime 1m

Inputs that fail are saved under the package's `testdata/fuzz/` and stay there as seeds once fixed.

## Conformance

Black-box tests of what Stalker clients see, over HTTP, against a running Overscan: [`conformance/README.md`](../conformance/README.md). They run with the other tests:

    go test ./conformance/...

## Who may call each route

`internal/server`'s route tests walk every route that the server serves, as its routers list them (`internal/routes`), and check each one by who may call it: anyone, the signed-in administrator, a box with a session, a Stalker client that the administrator approved, a player with a signed link, an account's token or login, or the admin token. A new route fails the tests until its author says who may call it, in `routeAccess`.

- Without what a route asks for, it refuses: the admin's pages send the browser to the sign-in page, and the APIs, the outputs, the links and the debug routes answer 401, 403 or 404. The outputs refuse players from outside the server's own networks before they read a token or a login.
- Every admin route that changes something refuses a request without the session's CSRF token, and one from another site.
- Every call of the Stalker API but the handshake needs the token that the handshake gave, from a device that the administrator approved.
- With the admin on an address of its own, the main address serves none of it.
- Every route that takes a name in its path is sent names that try to climb out of what it serves: escaped slashes and dots, and the absolute path of the key file. No answer may hold the key file, or the system's list of users.

They run with the other tests:

    go test ./internal/server -run 'Route|CSRF|Stalker'

## Soak test

`tools/soak` runs Overscan under load and records its memory and CPU use:

1. It serves a fake provider with a lineup of 20,000 channels in 200 groups, each an endless MPEG-TS stream at 5 Mbit/s, and their guide: a programme an hour for a day, 480,000 programmes in all. It writes a folder of 10,000 films and 500 series of 20 episodes each, as empty videos.
2. It runs Overscan with both as sources, built from the repository unless `-bin` names a binary, and lets it idle for 30 seconds once the lineup and its guide are loaded.
3. It simulates four boxes for ten minutes, half through the Stalker API and half as the MAG page through the device API. Each signs in, loads the whole channel list, then watches random channels, switching every two minutes on average; the Stalker boxes also make the keep-alive call, and those of the MAG page open *Movies* and read a page of its shelf of films before each channel.
4. Every five seconds it reads the server's resident memory and CPU time from `/proc`, so it runs on Linux only.

Run it from the repository:

    go run ./tools/soak
    go run ./tools/soak -duration 24h -boxes 8

| Flag | Default | Meaning |
| --- | --- | --- |
| `-duration` | `10m` | How long the boxes run |
| `-boxes` | `4` | How many boxes, each watching one stream |
| `-channels` | `20000` | How many channels the fake provider lists |
| `-guide` | `24` | Hours of one-hour programmes in the guide for each channel; 0 for no guide |
| `-films`, `-series` | `10000`, `500` | Films, and series of two seasons of ten episodes, in the folder |
| `-bitrate` | `5000000` | Each stream's rate, in bits per second |
| `-zap` | `2m` | How long a box stays on a channel, on average |
| `-bin` | | The Overscan binary to test, instead of building one |
| `-out` | `soak.csv` | Where the samples go |
| `-idle-mb`, `-peak-mb` | `60`, `150` | Memory budgets, in MB; 0 for none |
| `-cpu` | `0` | A budget for average CPU use under load, in percent of one core; 0 for none |

It prints a summary and writes the samples, and it fails if the server breaks a budget or if any stream or call fails. The memory budgets are the plan's: under about 60 MB idle, and under about 150 MB with a large catalog.

The *Soak* workflow runs the ten-minute version every Monday, and on demand from the repository's Actions tab for as long as asked, up to 330 minutes, since a job of GitHub's stops at six hours. It keeps the samples for 30 days. Hosted runners vary from run to run, so a run on the machine that Overscan will run on says more about it.

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

From 1.0 on, an upgrade keeps what a household set up ([D115](decisions.md)), and CI checks it with every change ([D118](decisions.md)). Its *Upgrades* job downloads the Linux binary of every full release from v0.3.0 on, and of the newest pre-release when it is newer than them all, with `tools/fetch-releases.sh`, and runs `tools/upgradetest` with each. For each release, the test:

1. serves a fictional provider, two playlists with a guide, logos and endless streams, and a folder of films and series;
2. runs the release on a new data directory, and sets up a household through its own APIs: sources, lineup edits and groups, the parental PIN, settings, a box paired by its code with favourites, recent channels, settings and resume points, a Stalker client, a blocked box, recordings to come and one called off, a shelf renamed and titles hidden and locked, and, as far as the release has them, profiles with a PIN and a theme, the household's end date, messages, a recording made and stopped, accounts with a login, a token, a limit, an end date, a PIN, an output profile and only some sources, a second lineup with rules, merge and guide rules, a channel's backup, and the outputs on;
3. records what the admin's API, the box, the Stalker client, and players through the Xtream login and the account's playlist, see;
4. starts the release again and records it all once more, to learn what changes by itself across a restart, such as when a source was last read;
5. starts this build on the same data directory: the administrator's password must still sign in, the box must still be paired, and every answer must be the same, with nothing missing, apart from the server's version and what step 4 found;
6. checks that the database was backed up before its schema changed ([D117](decisions.md)), that a copy of it upgraded by this build alone holds every row it had, in every table and column, and that its schema is a new database's.

To run it on your machine, with the binaries of the releases to upgrade from:

    tools/fetch-releases.sh /tmp/releases
    go run ./tools/upgradetest /tmp/releases/overscan-*

`-keep` keeps each upgrade's folder, with the servers' logs and databases, and `-v` lists what step 4 left out. Each release takes under a minute.

Every migration from 43 on also has a test of its own in `internal/store`, `TestMigrationNNNN…`, which makes a database with the schema before it, holding what a household sets up, applies the migration and checks that nothing was lost or changed: `TestNewMigrationsHaveUpgradeTests` fails without one. `TestMigrationsAreUnchanged` holds every migration to its SHA-256 in `internal/store/testdata/migrations.sha256`, since a migration that a release holds is never edited.

Before 1.0, each release was tried by hand as an upgrade of the one before: the previous release read a source and got an approved device, and the new release, started on the same data directory, had to give a Stalker client the same channels, with the same numbers. For v0.1.0 this was done from v0.0.9 and from v0.0.17, and for v0.4.0 and v0.5.0 from each pre-release before them and from the full release before.

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
- Green in Live TV records the channel shown, with or without a guide, and green again stops it; what it recorded plays on *Movies*. A box's *Settings → Recording Stops* chooses whether such a recording stops at the programme's end or when it is stopped.
- *Recordings* on Home lists what records now, what is to come and what ended in the last day; *Record by Time* records a channel from a start to an end, and the recording begins and ends at those times.

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
