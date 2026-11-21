# Security

How Overscan protects a household's server, what it leaves to the network it runs on, and what the review before v0.1.0 checked. [`SECURITY.md`](../SECURITY.md) says how to report a vulnerability, and the plan's section 11 is the design.

## The model

Overscan runs on a home network for the household's own devices. It trusts the administrator, who signs in to the admin, and the sources that the administrator adds, as far as fetching what they point to. Everything else is untrusted: devices that have not been approved, whatever a source sends, and the rest of the network.

## What is in place

| Area | In place |
|---|---|
| Network | Plain HTTP on port 8080, for the home network. `admin_listen` can give the admin an address of its own, such as `127.0.0.1:8081` reached over SSH (D44). Do not expose Overscan to the internet; if you must, put it behind a reverse proxy with TLS, named in `trusted_proxies` (D119). |
| Devices | A new device waits for approval. The MAG page pairs with a secret that the server keeps hashed, and the Stalker API refuses the MAC address of a paired box (D39). Stalker tokens are kept hashed. |
| Admin | An argon2id password; sessions in the database behind `HttpOnly`, `SameSite=Strict` cookies, `Secure` when the request came over TLS, or through a trusted reverse proxy that says it did (D119); CSRF tokens on every change; sign-ins slowed after failures; a content security policy with no inline scripts, `frame-ancestors 'none'` and `nosniff`. |
| Secrets | Source addresses and logins, and stream addresses that hold them, encrypted with AES-256-GCM under a key file kept outside the database (D46). They never reach a device, a page, a log or an error message, and ffmpeg never sees them. Tokens and secrets are compared in constant time. |
| Files | Readable by their owner only, whatever the umask: the database and its companions, the key file, backups (0600 in 0700 folders). The systemd unit adds its sandbox: no privileges or capabilities, a read-only system, private `/tmp` and devices, three address families, no new namespaces, no writable and executable memory. The Docker image runs as its own user on a read-only file system with no capabilities. |
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
| Outputs | Off until the administrator turns them on, and each account's too (D103, D104). They answer the server's own networks only (private, loopback, link-local, and the shared space of RFC 6598) until *Answer players anywhere* is on; the address checked is the connection's, never a header's. A player sees Overscan's own IDs and addresses, never a source's, and every stream plays through the hub within its account's limit and its source's. Playlists, guides and the API's answers are never cached. Turning a switch off, making new addresses, changing a login or ending an account stops what it let players play (D105). |
| Rules and backups | A rule's pattern is a wildcard or a regular expression of Go's, which runs in time linear in the text, of at most 200 characters; a lineup has at most 200 rules (D107). A rule's logo is an http or https address fetched through the artwork proxy, as a source's logos are (D54). A channel's backup plays within its own source's limit (D109). |
| Lineup files | Read only from the signed-in administrator, as a form's file or the API's body, and only to add new lineups (D112). A file is measured before YAML's parser reads it: at most 2 MB, as many tokens as such a file holds, and 32 levels deep, since the parser's time and memory grow with the square of how deeply a file nests. Anchors, aliases, merges, several documents and unknown fields are refused, and every value is checked as the admin checks it. A file holds no address, login, token or password. |
| Live transcodes | Off until the administrator chooses a profile that transcodes and gives *Transcoding* a number at a time (D110), with the same ffmpeg as every other transcode. |
| Content from sources | Size caps on everything read: playlists 256 MiB, HLS playlists 2 MiB, logos 4 MiB and 25 megapixels, guides, request bodies. The parsers of M3U, XMLTV and HLS playlists, the stream analyser and the repeat filter are fuzzed. Logos are converted by Overscan and SVG is refused (D54). |
| ffmpeg | Run with an argument list, never a shell; it reads and writes pipes only (`-protocol_whitelist pipe`), with an empty environment; at most eight at once; stopped with the stream, and with Overscan (D57). |
| Logs | No source addresses, logins, tokens, serial numbers or signatures, and no address of an output, which holds a token or a login. Players' and boxes' IP addresses appear, and MAC addresses, so that a device can be approved from the log; the support bundle takes out IP addresses and the outputs' paths, and keeps only a MAC address's maker's half. The capture log keeps no value of a parameter that can carry a login, a token or what identifies a device (D111). |
| Dependencies | Five direct Go modules besides the standard library, pinned in `go.sum`, checked by `govulncheck` in CI. No telemetry. |

## Known limits in v0.1.0

- Overscan speaks plain HTTP; TLS is a reverse proxy's job. Behind one, the admin's cookie lacks the `Secure` flag, since Overscan sees plain HTTP, unless the proxy is named in `trusted_proxies` and says that the browser came over HTTPS (D119).
- A source can make Overscan fetch any http or https address, addresses on the home network included: redirects, HLS segments and keys, logos. What comes back goes only to the household's devices, but the request is made. An option to refuse private addresses for a source is planned (plan, section 11).
- An M3U source can be a local file, which the admin can name; Overscan reads it as its own user, and only playlist entries come out of it. It must be a regular file: a named pipe, a device or a folder is refused, and what goes wrong is said without the file's path.
- The probe page's report address takes reports from anyone on the server's own networks, at most 100 of 256 KiB each, but only while `probe_reports` is on, which it is not by default (D120).
- Once a device is approved, its MAC address is all that a Stalker client needs, as the protocol has it. The MAG page adds its pairing secret.

## Known limits of the outputs

- Players that read a playlist, and those that sign in to an Xtream server, carry their token or their login in every address they ask for, as their protocols have it. Whoever sees an address can play the account's channels, so the outputs answer the server's own networks only, until told otherwise.
- Behind a reverse proxy that is not named in `trusted_proxies`, every player seems to come from the proxy's address. The check of the server's own networks then lets every player through, and the proxy has to decide who reaches the outputs. Wrong logins from anyone through the proxy also make every player's logins wait, since they count by address. Named there, the proxy says who each player is, and the check and the waits go by that player (D119).
- Someone who sends 50 wrong logins in 15 minutes, from ten addresses or more, makes every player's login wait until the oldest is 15 minutes old, as the admin's sign-in does. What plays goes on, and players that use a playlist's token are not affected.
- Overscan tells players apart by their address and their user agent, so two players behind one address with the same user agent count as one against the account's limit.

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
