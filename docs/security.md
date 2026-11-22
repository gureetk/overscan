# Security

How Overscan protects a household's server, what it leaves to the network it runs on, what is known not to be covered, and what each review checked, the last of them the review of the whole server for 1.0. Every review has been the project's own; none has been made from outside. [`SECURITY.md`](../SECURITY.md) says how to report a vulnerability, and the plan's section 11 is the design.

## The model

Overscan is made for a home network and the household's own devices (D128). Away from home, those devices reach it through a VPN into the home network, which makes them part of it, rather than through a port opened to the internet. It trusts the administrator, who signs in to the admin, and the sources that the administrator adds, as far as fetching what they point to. Everything else is untrusted: devices that have not been approved, whatever a source sends, and the rest of the network.

### Who can reach what

| Who | What they reach | What guards it |
|---|---|---|
| Anyone who reaches the server's port | The health check; the MAG page and its files; the probe page; the admin's sign-in page; a box's or a Stalker client's first request, which only records it as waiting for approval; the outputs' addresses, which answer nothing without a token or a login; pictures, by keys that only the lists that show them give | The administrator's approval of each device; argon2id and slowed sign-ins; the outputs' switch and the check of the server's own networks; signed links for streams |
| A box running Overscan's page, once approved | Its account's lineup, guide, films and series, recordings, messages, profiles and its own settings, through the device API | A session token, and a pairing secret beside its MAC address (D39); its account's end date, sources, limit and locks |
| A Stalker client, once approved | Its account's channels, guide, films and series, through the Stalker API | Its MAC address, and the token of its handshake; the MAC address of a paired box is refused (D39) |
| A player of the outputs | Its account's playlist, guide, Xtream API and streams | Off by default; a token or a login; the server's own networks unless told otherwise; its account's limit and its sources' (D103 to D105) |
| The administrator | Everything that Overscan does: sources, among them the files and folders on the server that its user can read, devices, accounts, settings | Trusted: signs in with the password set on the server |
| A source | What it sends: lists, guides, streams and pictures, and the addresses that Overscan then fetches | Size caps and fuzzed parsers; its addresses and logins never leave the server; its text is shown as text; public addresses only, if set (D121) |
| The internet beyond the home | Nothing, unless the administrator exposes the server | Made for the home network, and not for the internet (D128): a VPN into the home network instead; if the server is exposed anyway, a reverse proxy with TLS, named in `trusted_proxies` (D119), at the least |

## What is in place

| Area | In place |
|---|---|
| Network | Plain HTTP on port 8080, for the home network. `admin_listen` can give the admin an address of its own, such as `127.0.0.1:8081` reached over SSH (D44). Do not expose Overscan to the internet: away from home, reach it through a VPN into the home network (D128). A reverse proxy with TLS, named in `trusted_proxies` (D119), can give it a name and a certificate on the home network; exposing it through one is outside what Overscan is made for. |
| Reverse proxies | Believed only when named in `trusted_proxies`, each an address or range on the server's own networks (D119). The client that such a proxy names in `X-Forwarded-For`, read from the right past the trusted proxies, is the one that the log, the waits after wrong passwords and the outputs' check go by; one that names no client is no player at home. Its `X-Forwarded-Proto: https` makes the admin's cookie `Secure`. From anyone else, both headers are ignored. |
| Requests | Headers within 10 seconds and 64 KiB; a body within a minute of its first byte; a connection idle for two minutes is closed. Bodies are capped where they are read: 64 KiB for the admin's API and its forms, with a lineup file's 2 MB besides; 4 KiB for the device API; 16 KiB for an Xtream login's form. Answers take as long as they need, since a stream goes on for as long as someone watches. |
| Routes | Every route of both addresses is in a table of who may call it, and tests that walk the routers check each against it: the wrong caller is refused, every change in the admin needs the session's CSRF token and refuses another site, and names in paths that try to climb out of what a route serves reach no file ([testing.md](testing.md), *Who may call each route*). A new route fails the tests until it is in the table. |
| Devices | A new device waits for approval. The MAG page pairs with a secret that the server keeps hashed, and the Stalker API refuses the MAC address of a paired box (D39). Stalker tokens are kept hashed. |
| Admin | An argon2id password; sessions in the database behind `HttpOnly`, `SameSite=Strict` cookies, `Secure` when the request came over TLS, or through a trusted reverse proxy that says it did (D119); CSRF tokens on every change; sign-ins slowed after failures; a content security policy with no inline scripts, `frame-ancestors 'none'` and `nosniff`. |
| Secrets | Source addresses and logins, and stream addresses that hold them, encrypted with AES-256-GCM under a key file kept outside the database (D46). They never reach a device, a page, a log or an error message, and ffmpeg never sees them. Tokens and secrets are compared in constant time. |
| Files | Readable by their owner only, whatever the umask: the database and its companions, the key file, backups (0600 in 0700 folders). The systemd unit adds its sandbox: no privileges or capabilities, a read-only system, private `/tmp` and devices, three address families, no new namespaces, no writable and executable memory, and the system calls of ordinary services only (D126). The Docker image runs as its own user on a read-only file system with no capabilities. |
| Streams | Links signed with HMAC, bound to the device and the channel, valid for a minute (D21). The hub proxies every stream, so upstream addresses stay on the server (D20), and keeps each source's limit (D36). |
| Recordings | Off until the administrator gives them room (D89). A recording plays through the hub as any stream does, within its source's limit, and never ends a device's stream. Files are written only in the recordings folder, under names made of a programme's and a channel's letters, digits and a few marks, never a path from a source, a guide or a device; the folder's room is a cap that recordings stop at. A box schedules and calls off only its own household's recordings, at most 100 to come; deleting is the admin's. A recording whose source refuses it is asked for less and less often, up to once a minute. |
| Timeshift | Off until the administrator chooses how much to keep (D92). A buffer belongs to one box's session, whose link is signed for that box; its files are named by the server, in `timeshift` in the data directory, which the server empties of old buffers as it starts. Each buffer keeps at most what 20 Mbit/s takes in the time set, and drops its oldest file when the disk has less than 1 GB free. The session counts against its source's limit while it reads from it, ends 30 seconds after the box's last request or hold, and asks a source that breaks off again less and less often, up to every 30 seconds. `behind` is a number of seconds, cut to the time kept. |
| Shared tuner | Off until the administrator turns it on (D93). A shared channel's buffer is a timeshift buffer, with the same limits; its tuner counts once against its source's limit while any screen plays from it, and ends with its last screen, or when another stream needs the connection that no screen uses. Each screen still needs a link signed for its device, and only a box's link may go back in the buffer. |
| Parental PIN | Hashed with argon2id; after five wrong PINs in 15 minutes a box waits five minutes (D55). |
| End date | Past an account's end date its boxes get no session and every call of theirs is refused, its Stalker clients are refused as blocked ones are, and no recording of it starts (D95). A stream already playing stops at the device's next call. |
| Messages | Only the administrator sends them (D96). A box shows a message's text as text, never as markup, and gets only the messages sent to it; a message has at most 500 characters, and at most 50 wait at a time. |
| Themes | Only the administrator installs them (D97). A theme is data, never code: a few named values, each checked against what it may be (colours, a font or corners from a short list, a brand of letters, digits and a few marks), in a file of at most 16 KB, from which the server writes the stylesheet itself. Colours must keep text, the focus and the accent readable. A theme's stylesheet is served without a session, as the page's own is, under a name that only the boxes are told; it holds colours, a few choices and the brand. |
| Profiles | A profile's PIN is hashed with argon2id as the parental PIN is, and wrong ones count with the parental PIN's (D94). A box switches only among its own account's profiles, and a switch drops the parental PIN it kept. A profile's PIN is no lock: the locks stay the account's, whatever the profile. Profile names are shown as text, never as markup, on the box and in the admin. |
| Accounts and logins | A player's password is hashed with argon2id, as the administrator's is, and is never the administrator's or a source's; wrong logins slow down as the admin's sign-in does, five from an address and 50 in all within 15 minutes, counted apart from it (D102). A right login is remembered in memory for 15 minutes, under an HMAC whose key lives only in memory, with the hash it was checked against, so that a new password stops it at once. A playlist token holds 256 random bits, kept as its SHA-256 and sealed with the key file; new addresses stop the old ones. An account's limit on its streams is checked before its sources' (D36). |
| Outputs | Off until the administrator turns them on, and each account's too (D103, D104). They answer the server's own networks only (private, loopback, link-local, and the shared space of RFC 6598) until *Answer players anywhere* is on; the address checked is the connection's, or the client that a reverse proxy named in `trusted_proxies` names (D119), never a header from anyone else. A player sees Overscan's own IDs and addresses, never a source's, and every stream plays through the hub within its account's limit and its source's. Playlists, guides and the API's answers are never cached. Turning a switch off, making new addresses, changing a login or ending an account stops what it let players play (D105). |
| Rules and backups | A rule's pattern is a wildcard or a regular expression of Go's, which runs in time linear in the text, of at most 200 characters; a lineup has at most 200 rules (D107). A rule's logo is an http or https address fetched through the artwork proxy, as a source's logos are (D54). A channel's backup plays within its own source's limit (D109). |
| Lineup files | Read only from the signed-in administrator, as a form's file or the API's body, and only to add new lineups (D112). A file is measured before YAML's parser reads it: at most 2 MB, as many tokens as such a file holds, and 32 levels deep, since the parser's time and memory grow with the square of how deeply a file nests. Anchors, aliases, merges, several documents and unknown fields are refused, and every value is checked as the admin checks it. A file holds no address, login, token or password. |
| Live transcodes | Off until the administrator chooses a profile that transcodes and gives *Transcoding* a number at a time (D110), with the same ffmpeg as every other transcode. |
| Content from sources | Size caps on everything read: playlists 256 MiB and 50,000 channels, HLS playlists 2 MiB, logos 4 MiB and 25 megapixels, guides, request bodies. Every parser of what comes from outside is fuzzed ([testing.md](testing.md), *Fuzzing*). Logos are converted by Overscan and SVG is refused (D54). Text from a source is shown as text, and what goes into the log loses its control characters and the marks that change the direction of text. A source kept to public addresses connects to nothing on the server's own networks, whatever its lists and redirects say (D121). |
| Local files | A folder source is opened as a root that nothing in it can lead out of, symbolic links included (D67). A playlist file must be a regular file, and only playlist entries come out of it. Recordings and timeshift buffers are written under names that the server makes, never under a path from a source, a guide or a device. |
| Probe page | Saves its reports only while `probe_reports` is on, and only from the server's own networks: at most 100, of 256 KiB each (D120). |
| ffmpeg | Run with an argument list, never a shell, and an empty environment; at most eight at once; stopped with the stream, and with Overscan (D57). To repair a stream or transcode a live channel, it reads and writes pipes only (`-protocol_whitelist pipe`). To convert or transcode a film, it and ffprobe read the file from the hub over HTTP on the loopback address, at an address that holds a random secret, with `-protocol_whitelist http,tcp` and the demuxers of video files only, so that a file which is in truth a playlist cannot make them open other addresses (D68). |
| Logs | No source addresses, logins, tokens, serial numbers or signatures, and no address of an output, which holds a token or a login. Players' and boxes' IP addresses appear, and MAC addresses, so that a device can be approved from the log; the support bundle takes out IP addresses and the outputs' paths, and keeps only a MAC address's maker's half. The capture log keeps no value of a parameter that can carry a login, a token or what identifies a device (D111). |
| Dependencies | Five direct Go modules besides the standard library, pinned in `go.sum`, checked by `govulncheck` in CI. No telemetry. |

## Known limits

What Overscan does not protect against, as of 1.0, and why:

- **Plain HTTP.** TLS is a reverse proxy's job. Behind one, the admin's cookie lacks the `Secure` flag, since Overscan sees plain HTTP, unless the proxy is named in `trusted_proxies` and says that the browser came over HTTPS (D119).
- **What a source points at.** A source can make Overscan fetch any http or https address, addresses on the home network included: redirects, HLS segments and keys, logos. What comes back goes only to the household's devices, but the request is made. A source that the administrator keeps to public addresses cannot (D121): every connection made for it is checked once its name is looked up, and refused if the address is on the server's own networks or reserved. It is off by default, since Jellyfin, Plex and many playlists are on the home network, and a change applies from the next request.
- **Files on the server.** An M3U source can be a local file, and a folder source any folder, that the administrator names; Overscan reads it as its own user, within what the systemd unit lets that user see, with no list of allowed folders. Only playlist entries come out of a playlist file, which must be a regular file: a named pipe, a device or a folder is refused, and what goes wrong is said without the file's path.
- **A Stalker client's identity.** Once a device is approved, its MAC address is all that a Stalker client needs, as the protocol has it, and Stalker clients have no login of their own. The MAG page adds its pairing secret.
- **Devices waiting for approval.** Anyone who reaches the server can fill the list of devices that wait for approval, which holds at most 100, by sending made-up MAC addresses. A new box is then not listed; the administrator approves it by its MAC address on *Devices*, or removes those waiting.
- **Credentials in the outputs' addresses.** Players that read a playlist, and those that sign in to an Xtream server, carry their token or their login in every address they ask for, as their protocols have it. Whoever sees an address can play the account's channels, so the outputs answer the server's own networks only, until told otherwise.
- **Behind a reverse proxy that is not named.** Every player seems to come from the proxy's address. The check of the server's own networks then lets every player through, and the proxy has to decide who reaches the outputs; wrong logins from anyone through it make every player's logins wait, since they count by address. Named in `trusted_proxies`, the proxy says who each player is, and the check and the waits go by that player (D119).
- **Wrong logins from many addresses.** Someone who sends 50 wrong logins in 15 minutes, from ten addresses or more, makes every player's login wait until the oldest is 15 minutes old, as the admin's sign-in does. What plays goes on, and players that use a playlist's token are not affected.
- **Players behind one address.** Overscan tells players apart by their address and their user agent, so two players behind one address with the same user agent count as one against the account's limit.
- **The probe page.** While `probe_reports` is on, anyone on the server's own networks can save up to 100 reports of 256 KiB in the data directory (D120). It is off by default.
- **The administrator's own text.** Names and messages that the administrator writes may hold the marks that change the direction of text; only text from sources has them taken out. Only the administrator writes such text, and boxes show it as text.

## For an outside reviewer

A short guide for someone reviewing Overscan from outside, which no one has done yet.

- **Scope.** The server (`cmd/` and `internal/`, in Go), the MAG page (`web/mag`, TypeScript compiled to ES5), the admin's pages (`web/admin`), the probe page (`web/probe`), the install script, the Debian package's scripts and the systemd unit (`deploy/`), and the release (`tools/build-release.sh`, `.github/workflows/`). Out of scope: providers, media servers, the boxes' firmware and the household's network.
- **The model** is *Who can reach what*, above, and the plan's section 11 is the design.
- **Running it.** [trying-it.md](trying-it.md) starts a server and adds a source; [testing.md](testing.md) runs the tests, the conformance suites for the Stalker and Xtream APIs, the fuzzers and the upgrade test. `go test ./internal/server -run 'Route|CSRF|Stalker'` runs the route tests alone.
- **Where to look first.**
  1. Who may call what: the route table in `internal/server/routes_test.go`, and the checks it tests: `internal/admin/auth.go` (sessions, CSRF, sign-ins), `internal/portal/device` (sessions, pairing), `internal/portal/stalker` (handshakes and MAC addresses), `internal/portal/output` (the outputs' gate), and `internal/clientaddr` (reverse proxies).
  2. What a source can make the server do: `internal/hub` (proxying, the HLS relay, redirects), `internal/netguard` (public addresses only), `internal/catalog`, `internal/guide`, `internal/artwork` and `internal/adapters`.
  3. The parsers of outside input, listed with their fuzz tests in testing.md.
  4. Secrets: `internal/secrets` and the sealed addresses in `internal/store/sources.go`; the log's redaction, `internal/logging`; the support bundle, `internal/admin/support.go`.
  5. Files and processes: `internal/recorder`, timeshift in `internal/hub`, `internal/adapters/folder`, the backups in `internal/store`, ffmpeg's arguments in `internal/media`, `internal/hub/repair.go` and `internal/streamprofile`, and the unit in `deploy/systemd`.
- **Reporting** is as [SECURITY.md](../SECURITY.md) says: privately, through GitHub's vulnerability reporting.

## Dependencies and licences

Overscan's own code is under the AGPL-3.0-or-later, and the conformance suites and protocol notes under the Apache-2.0 (each file says). What the binary is built from besides the Go standard library (BSD-3-Clause):

| Module | Licence | For |
|---|---|---|
| `github.com/ncruces/go-sqlite3` | MIT | SQLite |
| `github.com/ncruces/go-sqlite3-wasm` | MIT No Attribution for its own code; the code that it translates keeps its authors' licences, and SQLite is in the public domain | SQLite itself, translated to Go |
| `github.com/ncruces/julianday` | MIT | SQLite's dates |
| `github.com/goccy/go-yaml` | MIT | The configuration file and lineup files |
| `golang.org/x/crypto` | BSD-3-Clause | argon2id |
| `golang.org/x/term` | BSD-3-Clause | Reading a password at the terminal |
| `golang.org/x/text` | BSD-3-Clause | Subtitles in older character sets |
| `golang.org/x/sys` | BSD-3-Clause | Used by the modules above |

Each licence allows Overscan's. The release's archive holds each module's licence in `licenses/`, copied by `tools/build-release.sh` from the modules that the binary is built from. The admin's pages ship htmx 2.0.11, under the Zero-Clause BSD licence, as `web/admin/README.md` says; the MAG page ships no code but its own. Both use the font Instrument Sans, under the SIL Open Font License, which is beside it. The tools that build the MAG page (TypeScript, Babel, Rollup, ESLint) are not shipped. `go.sum` pins every module, and `govulncheck` checks them in CI with every change.

## The review before v0.1.0

Each row of the plan's section 11 was checked against the code. What changed:

- **Fuzzing.** New fuzz tests for HLS playlists, the stream analyser and the repeat filter, beside the M3U and XMLTV ones. Each fuzzer ran for at least 45 seconds without a crash. One finding: an initialisation vector that did not parse left part of a value behind. It did no harm, since such a playlist is not relayed, but it is now dropped.
- **Files.** SQLite created the database's shared-memory index with the umask's mode, so outside the systemd unit others could read it. Overscan now sets its umask to 077 (#62).
- **ffmpeg** got its pipes, allowlist, empty environment and cap with stream repair (#60).
- **Backups** are private and never overwrite a file (#62), and the support bundle is redacted (#63).

Candidates for later: refusing private addresses per source; a system call filter in the systemd unit (`SystemCallFilter=@system-service`), once tested in a container.

## The review in M5

What M5 added was checked against the plan's section 11, text and themes from the admin that boxes show above all:

- **Text from the admin on the boxes.** The page shows profile names and messages as text, never as markup, and tests send markup through both, in the admin and to the box. A profile's name has 1 to 32 characters and a message 1 to 500, without control characters but a message's line breaks.
- **Themes are data.** A theme file is checked field by field, and anything else in it is refused. Colours must be colours and keep text readable, the font and corners come from short lists, and the brand holds only letters, digits, spaces and a few marks, so it cannot leave its string in the stylesheet: a fuzz test checks that. The server writes the stylesheet itself, and a theme's stylesheet answers without a session only under its current name.
- **The admin's new forms and API** need the session's CSRF token, which their tests check. A theme file is read up to 16 KB, within the form's 64 KB.
- **What a device can reach.** A box reads and marks only its own messages, and switches only among its own account's profiles, with wrong profile PINs counted with the parental PIN's. Past the household's end date, a box gets no session and a Stalker client only its profile and the end date.
- **The shared tuner** stays off until the administrator turns it on.
- **The support bundle** now counts themes and messages, without their names or text.
- **Upgrades:** the database of v0.3.0 upgrades to v0.4.0 directly, as each of M5's pre-releases did from the one before.

Candidates for later: refusing the characters that change the direction of text in names and messages; a cap on how many themes may be installed.

## The review in M6

What M6 added was checked against the plan's section 11 and section 21.2: the outputs, which carry credentials in their addresses, above all.

- **Credentials in addresses.** No log line holds an output's path, a token or a login: the outputs log the account, the player's address and what it read, and their errors name what failed, never the request. The Xtream output's capture took out the login alone; it now drops the value of every parameter that can carry a secret, as the Stalker API's does, and keeps at most 30 names and 100 bytes of each value (D111). The support bundle takes out the outputs' paths too, wherever a line holds one, as a second line of defence.
- **Redaction in the support bundle.** Players' IPv6 addresses written without brackets went through; they are now taken out, with or without a zone, and so are IPv4 addresses that IPv6 maps.
- **On the server's own networks by default.** Every output, the Xtream API, `get.php`, `xmltv.php` and every stream path included, goes through one check of the outputs' switch and the connection's address before it reads a login or a token. A header such as `X-Forwarded-For` cannot make a player local; `X-Forwarded-Proto` changes only the addresses that the player that sent it is given.
- **Tokens and logins.** A playlist token is compared by its SHA-256, and making new addresses stops the old ones and what players play with them. Every path that takes a login, a stream's included, slows wrong ones down with the same count; a right one is remembered only with the hash it was checked against. A remembered login does not pass while logins wait, since that would let guesses through without the wait.
- **Limits.** Every stream that an output serves counts against its account's limit and its source's, a backup's against its own source's, and no output gives a source's address or login to a player.
- **An account's sources** (D114). An account that uses only some sources is held to them where it plays, not only in its lists: the hub looks up the account of every stream it opens, a channel's, a film's or a past programme's, passes over the streams of other sources and refuses what has none left, whatever link or ID a player sends. Changing an account's sources ends what it plays from the others, sessions that wait for a player to come back included.
- **Messages** reach every account's boxes, still as text, never as markup (D111).
- **Upgrades:** the database of v0.4.0 upgrades to v0.5.0 directly, through migrations 36 to 42, as each of M6's pre-releases did from the one before. A household that adds no account and turns on no output sees nothing change.
- **Lineup files** (D112) are YAML from outside, read by the admin. A fuzz test found the parser stalling on some files: its time and memory grow with the square of how deeply a file nests, and it takes a few hundred bytes for each token, so a file of a few hundred kilobytes could take gigabytes. A file is now measured before it is parsed, which keeps a crafted one to about 280 MB and a second at worst. An export holds no secret: sources are named, and channels known by their source's key.

## The review in M7, for 1.0

The whole server was checked against the plan's section 11 and the model above, and each earlier review's claims were checked again against the code. What changed:

- **Who may call each route.** Every route is in a table of who may call it, which tests check by walking the routers, as the plan's section 15 asks: the auth and path-traversal test set. They found no route open to a caller who should not reach it.
- **Fuzzing.** Every parser of outside input that had no fuzz test got one. Two findings, both fixed: a portal's date in a year before 1970 was taken for an account's end date; and text from a source kept the marks that change the direction of text, which could make a line of the log or the admin read otherwise than it is.
- **Limits on requests.** Headers, bodies and idle connections have limits of time and size (*Requests*, above). Before, a client could keep a connection open by sending a body slowly, headers could hold 1 MiB, and an Xtream login's form was read up to 10 MiB.
- **Reverse proxies** (D119). Behind a proxy, every request seemed to come from the proxy: the outputs' check let every player through, one client's wrong passwords made everyone behind it wait, and the admin's cookie lacked `Secure`. A proxy named in `trusted_proxies` now says who each client is.
- **The probe page's reports** (D120) were saved from anyone who reached the server; they are now saved only while the configuration says so, and from the server's own networks only.
- **Playlist files** must be regular files: a named pipe held a refresh up until something wrote to it, and a device gave data until the size limit stopped it. Errors no longer carry the file's path.
- **Public addresses only** (D121), which the review before v0.1.0 left for later, keeps a source's requests off the home network.
- **Mistakes in the configuration file** were reported with the lines around them, which can hold a source's password, and so reached the journal. The message now names the file, the line and the key, and no value (D125).
- **Claims checked again.**
  - Before v0.1.0: the *ffmpeg* row said that ffmpeg reads and writes pipes only, which has not been so since films are converted (D68); it now says what each use reads. Its candidate for later, a system call filter in the systemd unit, is in place, and CI tests it in a container (D126).
  - M5: the marks that change the direction of text are taken out of what sources send, but not of what the administrator writes (*Known limits*). Themes still have no cap on how many may be installed; each is at most 16 KB, and only the administrator installs them.
  - M6: a header such as `X-Forwarded-For` can now make a player local, but only from a reverse proxy named in `trusted_proxies`, which must be on the server's own networks. The rest held.
- **Upgrades** keep what a household set up, and CI checks it with every change, from every full release since v0.3.0 (D117, D118).
