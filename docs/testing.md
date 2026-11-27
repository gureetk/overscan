# Testing

How Overscan is tested, and how to run each kind of test. The [plan](plan.md), section 15, sets out the approach.

## On every pull request

CI checks formatting, module tidiness and vet, runs the Go tests with the race detector, the conformance suite and the fuzz tests' seeds among them, and checks licence headers, ES5 syntax in browser scripts, sign-offs and known vulnerabilities. It installs ffmpeg first, so that the stream repair tests run the real one. For the MAG page it type-checks the sources, builds the page, fails if the build differs from the committed `web/mag/dist/`, and lints the build. It also builds the Docker image from a release archive's files and checks it with `tools/check-image.sh`; builds the release's archives and Debian packages and, with `tools/check-packages.sh`, installs, upgrades and removes each on a fresh Debian system running systemd; and upgrades a household from every full release since v0.3.0 to the change, as [*Upgrades*](#upgrades) says. [CONTRIBUTING](../CONTRIBUTING.md) lists the commands to run the same checks locally.

## Fuzzing

Every parser of what comes from outside has a fuzz test, which checks that it never fails on what it is given and that what it keeps makes sense:

- what sources send: M3U playlists and their catch-up attributes, XMLTV guides, HLS playlists, MPEG-TS streams (their packets and tables, the stream analyser, the repeat filter, timeshift's places to start, and the video's parameter sets that give a stream's details), Xtream servers' answers (the account, categories, live streams, films and series), Stalker portals' answers (genres, channels, links, pages of films and series, guides and dates), Jellyfin's and Plex's items, the names and `.nfo` files in folders, subtitles, ffprobe's reports, the names of channels as guides of the household's own match them (D139) and as suggestions compare them (D140), and the text of a source's answers that goes into the log;
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
3. With `-own-guide`, before it idles, it signs in to the admin and adds a guide of the household's own ([D139](decisions.md)), which the fake provider serves: the provider's guide then lists only the first half of the channels, and the household's lists the second half by their names under IDs of its own, then channels that fill nothing, as many as `-own-guide` says in all, each with an icon and `-own-hours` hours of programmes from a day back. It waits for the guide's read and prints its log line. With `-placeholders`, it turns on placeholder programmes ([D142](decisions.md)) and the household's playlist and guide, then reads that XMLTV guide once.
4. It simulates four boxes for ten minutes, half through the Stalker API and half as the MAG page through the device API. Each signs in, loads the whole channel list, then watches random channels, switching every two minutes on average; the Stalker boxes also make the keep-alive call, and those of the MAG page open *Movies* and read a page of its shelf of films before each channel.
5. Every five seconds, and every second until the boxes start, it reads the server's resident memory and CPU time from `/proc`, so it runs on Linux only. The peaks it reports are the kernel's high-water mark of the server's memory where that is higher than the samples, so a peak between two samples counts.
6. With `-transcode`, the first channels, as many as `-pictures` says or as there are boxes, stream pictures that an ffmpeg of the soak's own makes as they are watched, at the pace that they play: ffmpeg's test pattern of `-picture-size` in H.264 at `-bitrate`, with AAC. Before it idles, the soak sets live TV to be transcoded always with the profile named ([D147](decisions.md)), and lets the server transcode as many streams at a time as there are boxes, up to 3; the boxes watch only those channels. At each sample it also reads the server's ffmpegs from `/proc`, the most memory one of them took and the processor that they took, and how fast each transcode goes, as the admin's list of streams says.

Run it from the repository:

    go run ./tools/soak
    go run ./tools/soak -duration 24h -boxes 8
    go run ./tools/soak -own-guide 20000 -own-hours 168 -placeholders 60
    go run ./tools/soak -transcode small -boxes 2 -channels 2000 -picture-size 1280x720

| Flag | Default | Meaning |
| --- | --- | --- |
| `-duration` | `10m` | How long the boxes run |
| `-boxes` | `4` | How many boxes, each watching one stream |
| `-channels` | `20000` | How many channels the fake provider lists |
| `-guide` | `24` | Hours of one-hour programmes in the guide for each channel; 0 for no guide |
| `-films`, `-series` | `10000`, `500` | Films, and series of two seasons of ten episodes, in the folder |
| `-own-guide` | `0` | Channels that a guide of the household's own lists, filling the half of the lineup that the provider's guide leaves out; 0 for none |
| `-own-hours` | `168` | Hours of one-hour programmes in it for each channel |
| `-placeholders` | `0` | Minutes of the placeholder programmes to turn on, 30, 60, 120 or 180, after which the XMLTV guide is read once; 0 for none |
| `-bitrate` | `5000000` | Each stream's rate, in bits per second |
| `-zap` | `2m` | How long a box stays on a channel, on average |
| `-bin` | | The Overscan binary to test, instead of building one |
| `-out` | `soak.csv` | Where the samples go |
| `-idle-mb`, `-peak-mb` | `60`, `150` | Memory budgets, in MB; 0 for none |
| `-cpu` | `0` | A budget for average CPU use under load, in percent of one core; 0 for none |
| `-transcode` | | Transcode live TV always with this profile: `h264-720p`, `h264-1080p`, or `small` (H.264 up to 576 lines, low quality) or `hevc-720p` (HEVC up to 720 lines, medium quality), which the soak makes; none at first |
| `-pictures` | as many as the boxes, with `-transcode` | How many channels, the first, stream pictures that ffmpeg makes |
| `-picture-size` | `1920x1080` | The size of those pictures |
| `-ffmpeg-mb` | `0` | A budget for one transcode's ffmpeg, in MB; 0 for none |

It prints a summary and writes the samples, and it fails if the server breaks a budget or if any stream or call fails. The memory budgets are the plan's: under about 60 MB idle, and under about 150 MB with a large catalog.

The *Soak* workflow runs it with a guide of the household's own of 20,000 channels with a week of programmes each, and hour-long placeholders (`-own-guide 20000 -own-hours 168 -placeholders 60`): for ten minutes every Monday, and on demand from the repository's Actions tab for as long as asked, up to 330 minutes, since a job of GitHub's stops at six hours. It keeps the samples for 30 days. A second job of the workflow, at the same times, soaks transcodes in software at a small size: two boxes on channels of 720p pictures that live TV transcodes always with `small` (`-boxes 2 -channels 2000 -transcode small -picture-size 1280x720 -ffmpeg-mb 400`). Hosted runners vary from run to run, so a run on the machine that Overscan will run on says more about it.

### Baseline

The budgets are the plan's, in its section 15: under about 60 MB of memory idle, under about 150 MB with a large catalogue, and a few percent of one core for a few streams.

**For 1.3, its transcodes**, in November 2026, on a cloud machine with two cores of an Intel Xeon at 2.1 GHz, without graphics, with ffmpeg 6.1. Live: three runs of four minutes with one box on channels of 1080p pictures at 5 Mbit/s, live TV transcoded always with each profile in software (`-duration 4m -boxes 1 -channels 2000 -films 500 -series 20 -transcode …`), against the server of v1.3.0-alpha.4's code. Films: ffmpeg run with the arguments that the hub gives it, on a minute of a fictional film read over HTTP, as fast as it goes, for D73's transcode of what a box cannot play and for a film set always with a small profile. The processor is given as a share of one core while the stream plays at its pace; memory is ffmpeg's most, apart from the server's.

| Transcode | Speed | Processor | ffmpeg's memory |
| --- | --- | --- | --- |
| Live, 1080p H.264 to *small* (H.264 up to 576 lines, low quality) | 1.00x, kept up | 27% of one core | 160 MB |
| Live, 1080p H.264 to *h264-720p* | 1.00x, kept up | 37% | 194 MB |
| Live, 1080p H.264 to *hevc-720p* (HEVC up to 720 lines, medium quality) | 1.00x, kept up | 102% | 264 MB |
| Film, 1080p H.264 at 10 bits in HDR10 to H.264 1080p made SDR, with Dolby Digital 5.1 (D73) | 1.26x as fast as it plays, flat out | 139% | 430 MB |
| Film, 1080p H.264 to *small*, with AAC | 5.5x, flat out | 33% | 172 MB |

The server itself stayed under 51 MB under load in each run, and idled at 36 to 37 MB; its peak while reading, about 101 MB, takes in the administrator's sign-in. Transcoding takes the processor and memory of ffmpeg, not of the server: an HEVC transcode needs a core of its own here, and mapping HDR to SDR in software needs more than one, with most of its memory in the pictures that it maps. On the graphics, ffmpeg's share of the processor falls to a few percent; those measures come from a server with Intel graphics, in M10's box tests. CI's *Soak* workflow soaks transcodes in software each week, and the image check prints the image's size, with and without its VA-API drivers: on amd64, 217 MB with them and 211 MB without, so Debian's two drivers add 6 MB.

**For 1.2**, in November 2026: a run of 60 minutes with a guide of the household's own (`-duration 60m -own-guide 20000 -own-hours 168 -placeholders 60`, the other flags at their defaults), on a cloud machine with two cores of an Intel Xeon at 2.1 GHz, and, as a second check, 120 minutes with the same flags by the *Soak* workflow on a GitHub-hosted runner (`ubuntu-latest`, Ubuntu 26.04). Both ran the server of v1.2.0-rc.1's code; rc.2 changed only the admin's *Guides* page. The guide's 1.68 million programmes were read in 53 and 40 seconds, and the XMLTV guide, 490 MB with the placeholders, in 13 and 9 seconds. From this release on, the peaks are the kernel's high-water marks.

| Measure | 60 minutes, two cores | 120 minutes, CI | Budget |
| --- | --- | --- | --- |
| Resident memory at the peak while reading the sources, the guide of the household's own and the XMLTV guide | 105.5 MB; 82.2 MB before the guide | 106.4 MB; 77.1 MB before the guide | 150 MB |
| Resident memory, idle with all of it loaded | 53.0 MB | 51.8 MB | 60 MB |
| Resident memory at the peak under load | 141.2 MB; the samples, at most 79.1 MB | 134.2 MB | 150 MB |
| Resident memory at the end | 69.1 MB | 65.6 MB | |
| CPU, average under load: four streams, 20 Mbit/s in all | 2.5% of one core | 1.7% | A few percent |
| CPU at the peak, over five seconds | 43.8% of one core | 43.6% | |
| Streams | 136 played, none failed, 9.0 GB proxied | 257, none failed, 18.0 GB | |
| Calls | 358, none failed; 62 shelves of films read | 693, none failed; 124 shelves | |

The budgets hold. The peak while reading takes in the administrator's sign-in, whose password hash takes 64 MiB for a moment; the server gives that back at once, and the memory of a written XMLTV guide too. The peak under load comes in the seconds when the four boxes load the whole lineup at once, each answer built in memory, as it has since the first run: the high-water mark sees it, where samples five seconds apart did not. Under load, the samples' means over the run's thirds were 60.4, 62.7 and 65.0 MB, and a second run of 35 minutes found 58.3, 59.1 and 58.9 MB.

**For 1.0**, in November 2026: a run of 330 minutes with the defaults, by the *Soak* workflow on a GitHub-hosted runner (`ubuntu-latest`, Ubuntu 26.04), and, as a second check, 150 minutes on a cloud machine with two cores of an Intel Xeon at 2.3 GHz. The first ran the server as released in v1.0.0-alpha.4, the second as in alpha.5, which changed no code that the test runs; neither is the LXC.

| Measure | 330 minutes, GitHub's runner | 150 minutes, two cores | Budget |
| --- | --- | --- | --- |
| Resident memory at the peak while reading the sources | 60.5 MB | 60.3 MB | 150 MB |
| Resident memory, idle with the sources loaded | 37.7 MB | 36.7 MB | 60 MB |
| Resident memory at the peak under load | 73.6 MB | 68.1 MB | 150 MB |
| Resident memory at the end | 60.9 MB | 55.4 MB | |
| CPU, average under load: four streams, 20 Mbit/s in all | 1.6% of one core | 1.4% of one core | A few percent |
| CPU at the peak, over five seconds | 35.4% of one core | 21.8% of one core | |
| Streams | 665 played, none failed, 49.4 GB proxied | 321 played, none failed, 22.5 GB proxied | |
| Calls | 1,829, none failed; 330 shelves of films read | 871, none failed; 158 shelves read | |

The budgets hold. Reading the sources, with the guide's 480,000 programmes, peaks at about 60 MB; the server hands that memory back to the system as soon as the sources are stored, and then idles at under 40 MB. Under load, the two-core run's samples stay between about 48 and 68 MB, with no growth from its first hour to its last. Peaks of CPU last a few seconds; the average stays under 2% of one core.

**The first run**, in November 2026: ten minutes with the defaults of the time, a lineup of 20,000 channels without a guide or a library, against the server as released in v0.0.4, on a cloud machine with two cores of an Intel Xeon at 2.1 GHz.

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

From 1.0 on, an upgrade keeps what a household set up ([D115](decisions.md)), and CI checks it with every change ([D118](decisions.md)). Its *Upgrades* job downloads the Linux binary of every full release from v0.3.0 on, and of the newest pre-release when it is newer than them all, with `tools/fetch-releases.sh`, and runs `tools/upgradetest` with each, straight to the change. Then it runs the test once more with `-chain`, along the path that [upgrading.md](upgrading.md) recommends: the household that v0.3.0 sets up goes through each full release in turn, and then to the change ([D129](decisions.md)). For each release, the test:

1. serves a fictional provider, two playlists with a guide, logos and endless streams, a second guide, and a folder of films and series;
2. runs the release on a new data directory, and sets up a household through its own APIs: sources, lineup edits and groups, the parental PIN, settings, a box paired by its code with favourites, recent channels, settings and resume points, a Stalker client, a blocked box, recordings to come and one called off, a shelf renamed and titles hidden and locked, and, as far as the release has them, profiles with a PIN and a theme, the household's end date, messages, a recording made and stopped, accounts with a login, a token, a limit, an end date, a PIN, an output profile and only some sources, a second lineup with rules, merge and guide rules, a channel's backup, the outputs on, a source that brings only the categories ticked, a lineup of the household's own groups whose group takes categories of two sources and has a first number, a merge that puts a source first and prefers the best picture, a channel hidden until it plays again, a guide of the household's own that fills three channels by name, channels whose guides were chosen by hand: a guide's channel, another channel's guide, and none, and logos: one set by hand, none for a channel, a guide's icons before channels' own, and a source's guide's where they are missing, two-hour placeholder programmes, TMDB's pictures for providers' titles turned on, a channel of one source moved to another's, and what is transcoded: a profile of the household's own, taken by an account, live TV never, a group always with it and a channel always with a fixed one, the graphics' number at a time, films and series always with it, a shelf never, a film always with a fixed one, and a series never;
3. records what the admin's API, the box, the Stalker client, and players through the Xtream login and the account's playlist, see;
4. starts the release again and records it all once more, to learn what changes by itself across a restart, such as when a source was last read;
5. with `-chain`, starts each newer release in turn on the same data directory, which brings the database up to date as that release would, and waits for it to read its sources;
6. starts this build on the same data directory: the administrator's password must still sign in, the box must still be paired, and every answer must be the same, with nothing missing, apart from the server's version and what step 4 found;
7. checks that this build backed up the database before it changed the schema ([D117](decisions.md)), that a copy of the oldest release's database, upgraded by the releases on the way and this build alone, holds every row it had, in every table and column, and that its schema is a new database's.

To run it on your machine, with the binaries of the releases to upgrade from:

    tools/fetch-releases.sh /tmp/releases
    go run ./tools/upgradetest /tmp/releases/overscan-*
    go run ./tools/upgradetest -chain /tmp/releases/overscan-v0.3.0 /tmp/releases/overscan-v0.4.0 /tmp/releases/overscan-v0.5.0

`-keep` keeps each upgrade's folder, with the servers' logs and databases, and `-v` lists what step 4 left out. Each release takes under a minute.

`tools/upgradetest` also walks through M8's *Done when* (the plan, section 14) with every `go test ./...`, in `TestPlaylistsDoneWhen`: with the same fictional provider and a third playlist, a source set to three of its categories keeps only their channels and their programmes; a lineup started empty, with a group that takes categories of three sources, holds exactly their channels, numbered from the group's first number, and a channel that a source adds appears after a refresh, numbered after the group's last; numbers stay unique; a merged channel plays the copy of the source put first, and then the best picture seen once two copies with real pictures have played; the lineup's page names the account and how it is reached; and its playlist and guide, its Xtream login, a box and a Stalker client show the same channels with the same numbers.

It walks through M9's *Done when* too, in `TestGuidesDoneWhen`: with two guides of the household's own, one of them with traps, and a box, the guides fill the channels whose sources give none, by guide ID and by name, and fill nothing wrong (a name in two countries that nothing chooses between, a longer name, channels that their sources or a guide before fill); a guide chosen by hand holds through a refresh of its source and a read of the guide; logos follow their order, by hand, a rule, the channel's own if it loads, its guide's if it loads, none, and a guide can give its logos first; placeholders show only while they are set, and only where a channel has nothing; and a lineup moved from the source main to a new provider's two sources keeps its numbers, groups, names, favourites and logos, plays from the new sources, and keeps what moved off main at its next read. The line about TMDB's pictures is walked in the library's tests, `TestPicturesDoneWhen`, against a fake TMDB: titles whose posters are missing, do not load or are shared by ten take TMDB's, and the others keep their own.

And M10's, in `TestTranscodingDoneWhen`, with ffmpeg's clips of 1080p pictures as two channels' streams, on a server without graphics: with nothing set, the account's playlist, its Xtream login, a box and a Stalker client each get the channel as its source sends it, through one connection to the source; a channel set always with a small profile reaches the playlist in 1024×576, through one connection, while an account whose own profile is *h264-720p* gets 1280×720; *never* reaches both as the source sends it; with live TV always and the group never, every output gets the source's stream, and with the channel always too, every output gets it transcoded; the server says that it has no graphics, in its log and its API; and with one transcode at a time, a second channel set always plays as its source sends it while the box plays the first. Films and series, each profile's arguments, the fallbacks of the graphics, and the packaging are walked in the tests that the walk's comment names.

And M11's, as far as its first phase goes, in `TestHLSDoneWhen`, with ffmpeg's clip as a channel's stream: while the account's *HLS for players* is off, its Xtream login offers only `ts`, a channel's `.m3u8` is not there and `get.php` gives `.ts` addresses; with it on, the login offers `m3u8` too, `get.php` and the playlist with `output=m3u8` give `.m3u8` addresses, and two players each get a playlist of one rendition whose media playlist lists segments that start with the PAT and the PMT and hold the channel's 1080p pictures, through one connection to the source, with no address that shows the source or the login; and turned off again, the players' HLS ends and `.m3u8` is as before. ffmpeg plays the HLS as a player, and sessions end once their players stop asking, in the hub's `TestCutHLS`; the playlist's rules, in `TestCutPlaylistRules`; and the cutting, in `internal/hlscut`'s tests.

Every migration from 43 on also has a test of its own in `internal/store`, `TestMigrationNNNN…`, which makes a database with the schema before it, holding what a household sets up, applies the migration and checks that nothing was lost or changed: `TestNewMigrationsHaveUpgradeTests` fails without one. `TestMigrationsAreUnchanged` holds every migration to its SHA-256 in `internal/store/testdata/migrations.sha256`, since a migration that a release holds is never edited.

## Graphics

CI has no graphics, so the transcodes on them (D148) are tested in two halves. The test at start runs against a stand-in for ffmpeg that answers as graphics would, or fails a step as they might (`internal/gpu`): which mode each codec encodes in, which rate controls hold, which decodes pass, and why a server has none to use; and against the real ffmpeg on a server without graphics. The hub's tests run a stand-in ffmpeg that notes how it was asked to transcode, on the graphics decoding there, on the graphics decoding in software, or in software, and fails as told (`TestGraphicsFallBack`, and `TestFilmGraphicsFallBack` for films): every fallback, the graphics' own number at a time, and the arguments that each way gets. A film's transcode with a profile runs through the real ffmpeg in software (`TestFilmAlways`), and the sound that each profile makes for a box that plays AAC and one that does not is checked in `internal/media`. A player's transcode at a constant rate (D151) runs through the real ffmpeg too: `TestSeekableWithFFmpeg` checks that its length is what it says and that the stream from a byte's time has the pictures of the stream from the start at that byte, and `TestFilmForPlayers` the answers to a player: the file as it is unless the title is set always, `HEAD`, the whole, the same session for the player's next request, a range from a byte at its time and in step with its packets, a range that ends, one past the end, ffprobe seeking to a time within seconds with the stream's end made once, and a link prepared again after a long pause. The arguments themselves are checked word by word in `internal/streamprofile`. What only real graphics can tell, that ffmpeg takes those arguments and that a transcode keeps up, is checked on a server with Intel graphics before each release of M10, with `overscan gpu` and a channel on each profile.

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
