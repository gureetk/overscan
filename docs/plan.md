# Overscan: project plan

**Status:** draft 2, 11 November 2026 (draft 1: 10 November 2026). Draft 2's proposals were accepted on 11 November 2026 and are recorded in [`decisions.md`](decisions.md) as D17 to D29; the probe results from the MAG322w1 added D32 and D33. M2's first decisions, D44 to D49, were taken on 13 November 2026.
**One line:** a free, self-hosted portal server that takes any source (Xtream, Stalker, M3U, Jellyfin, Plex, local files) and serves it to set-top boxes and players: a native UI for MAG boxes and a Stalker-compatible API first, then M3U/XMLTV, Xtream and HDHomeRun outputs, so one server can feed every player in the house.
**First target:** an Infomir MAG322w1, running Overscan in a Proxmox LXC.
**Companion docs:** [`protocol/stalker-protocol-notes.md`](protocol/stalker-protocol-notes.md) (what a Stalker client expects), [`clean-room.md`](clean-room.md) (how the protocol is documented), [`decisions.md`](decisions.md) (decision log).

---

## 1. What Overscan is

Operators of IPTV services run proprietary middleware to give set-top boxes a portal: a channel list, a guide, movies, series, favorites, catch-up. Home users who own those boxes (MAG boxes especially) cannot get that middleware, and most of the free tools around it are clients or proxies, not servers.

Overscan is the server. It ingests content from the sources people already have, merges it into one catalog, and publishes it through the protocols boxes and apps speak. A MAG box points at Overscan and sees a normal portal with the user's own channels, movies and series. Later, the same catalog feeds every other player in the house (section 21).

### Goals

| # | Goal |
|---|---|
| G1 | Serve MAG boxes (MAG322w1 first) with a native portal UI that works on their old browser, and MAG emulators such as StbEmu with the same page. |
| G2 | Serve Stalker-compatible clients through the Stalker API (the reference client first, defined in the glossary; OTT Navigator and TiviMate as compatibility targets). |
| G3 | Ingest from M3U/XMLTV, Xtream, upstream Stalker portals, Jellyfin, Plex and local files into one catalog. |
| G4 | Convert between protocols: Xtream to Stalker, Stalker to Xtream or M3U, M3U to Stalker, libraries to everything. Stalker output comes first; the other outputs arrive in the hub stage (M6). |
| G5 | Household features: devices, profiles, favorites, parental PIN, resume points, messages. |
| G6 | Easy to run: one binary, SQLite, a web admin, a documented LXC and Docker setup. |
| G7 | Clean-room, openly licensed, and documented well enough for others to contribute. |
| G8 | Later, act as the household's IPTV hub: every player in the house gets a curated lineup through the protocol it speaks, with per-person logins and rule-based customization (section 21). |

### Non-goals (v1)

- Reseller, billing and multi-tenant operator features. They are a large scope and are the part of Ministra most tied to commercial operations. They stay out in the hub stage too: the Xtream-compatible output is for the server's own users, not for running a panel.
- DRM, conditional access or anything that circumvents provider restrictions.
- Hosting, supplying or recommending content. Users bring their own lawful sources.
- Replacing Infomir's firmware or building custom firmware images.
- Native mobile apps. Existing players and the reference client are the clients.

### Principles

1. **Household first.** A home with a handful of devices, not a service with thousands of subscribers.
2. **The server owns the truth.** Upstream URLs and credentials never reach a client. Clients get short-lived links minted by Overscan, and streams are proxied unless a source is marked as safe to redirect.
3. **Resolve at play time.** Links expire upstream, so Overscan asks for them when someone presses play.
4. **Be lenient in, strict out.** Parse messy upstream data defensively and emit clean, predictable answers.
5. **Prefer not transcoding.** Redirect, proxy or remux first; transcode only when a device cannot play the source.
6. **Light enough for an LXC.** Budgets for RAM and CPU are part of the design (section 15).
7. **Clean room.** See section 17.
8. **Interoperate before rebuilding.** Where a mature tool already does a job well (pseudo-channels, heavy stream management), Overscan reads its M3U, XMLTV or Xtream output instead of reimplementing it.

---

## 2. Scenarios

| # | Scenario | What it needs |
|---|---|---|
| S1 | Repurpose a MAG322w1 as a front-end for Jellyfin, Plex and local files. | Library adapters, MAG UI, remux/transcode, resume. |
| S2 | Use an Xtream subscription on a box that only speaks Stalker. | Xtream adapter, Stalker API server, device registry. |
| S3 | Merge several playlists and accounts into one lineup with one guide. | Lineup layer, EPG matching, M3U and Stalker adapters. |
| S4 | Give every device at home its own favorites, PIN and resume points. | Profiles, per-device state. |
| S5 | Feed the reference client (or another app) from the same server. | Stalker API conformance. |
| S6 | Give every player in the house (TiviMate, Kodi, VLC, Plex or Jellyfin Live TV) the same curated lineup, each person with their own login. | Accounts, M3U/XMLTV and Xtream output, lineup rules (the hub stage, M6). |

**Order.** Live TV (S2, S3) comes before libraries (S1) because it needs no transcoding and exercises the whole path from source to box. If S1 matters more, M2 and M3 can swap; nothing in the architecture depends on the order.

**Terms of service.** Users are responsible for using their subscriptions as the provider allows. Overscan always enforces each source's own connection limit and never exceeds it. Sharing one upstream stream between several local viewers ("shared tuner" mode) is off by default and documented as the user's call. Outputs count too: every stream an Overscan output serves counts against its source's limit.

---

## 3. Compatibility surfaces

| Surface | Consumers | Contract | Priority | From |
|---|---|---|---|---|
| MAG portal UI (served at `/c/`) and its device API | MAG322w1; MAG emulators such as StbEmu | Our own design; the device API is documented in `docs/api/device.md` | P0 | M1 |
| Stalker API (`/portal.php`, `/stalker_portal/server/load.php`) | The reference client and Stalker clients with their own UI (OTT Navigator, TiviMate) | `stalker-protocol-notes.md` and the conformance suite | P0 | M1 |
| Admin web UI and REST API | The administrator | OpenAPI document | P0 | M2 |
| M3U + XMLTV output | Any player; Plex and Jellyfin Live TV | Standard formats | P1 | M6 (an admin-only debug playlist in M1) |
| Xtream-compatible output (`player_api.php`, `xmltv.php`, stream paths) | Xtream players | Community documentation, to be written up under `docs/protocol/` | P1 | M6 |
| HDHomeRun emulation | Plex, Jellyfin and Emby Live TV | Community documentation of the HTTP endpoints | P2 | M6 (stretch) |
| Ministra-style management REST (accounts, tariffs, events) | Scripts and glue | Infomir's public REST docs | P3, optional | Backlog |

The MAG itself only loads a web page, so for MAG-native support we write both ends of that conversation. The Stalker API matters most for third-party clients, which expect Ministra-style answers exactly.

MAG emulators are a different case. StbEmu loads the portal's own web page and emulates the box's JavaScript API (it can emulate a MAG322), so it exercises our MAG UI, not the Stalker JSON API ([StbEmu docs](https://docs.stbemu.com/)). Clients with their own UI, such as OTT Navigator and TiviMate, call the Stalker API directly.

---

## 4. Architecture

```
        sources                       Overscan                    devices and players
  ┌──────────────────┐   ┌────────────────────────────────┐   ┌──────────────────────────┐
  │ M3U + XMLTV      │   │ adapters ─► catalog ─► lineup  │   │ MAG322w1 (our web UI)    │
  │ Xtream           │──►│ EPG engine (UTC inside)        │──►│ Stalker clients          │
  │ upstream Stalker │   │ accounts, devices, profiles,   │   │ Xtream players (M6)      │
  │ Jellyfin / Plex  │   │   parental controls            │   │ M3U/XMLTV players (M6)   │
  │ local files      │   │ portals: device API, Stalker,  │   │ Plex/Jellyfin Live TV    │
  └──────────────────┘   │   M3U/XMLTV, Xtream, HDHomeRun │   │   via HDHomeRun (M6)     │
           ▲             │ stream hub: links, sessions,   │   └──────────────────────────┘
           └─────────────┤   limits, proxy, failover ─────┼──► ffmpeg (remux, transcode)
                         │ admin UI + REST API + jobs     │
                         └────────────────────────────────┘
```

### Components

- **Source adapters.** One per source type behind a common interface: validate, refresh catalog, resolve a playable stream at play time. They never talk to devices. Each declares what it mirrors and what it fetches on demand, whether its URLs carry secrets, and its connection and request limits.
- **Catalog.** Normalized channels, programmes, movies, series, seasons, episodes, media files and categories, stored in SQLite. Every item keeps its source and source id.
- **Lineup.** An editorial layer over the catalog: renumber, rename, regroup, hide, merge duplicates, map to guide data. Devices see the lineup, not raw source data, and the lineup also says what an account may see (D45). Edits survive refreshes from M2 (D47); in the hub stage they become rules, and a household can keep several lineups (section 21).
- **EPG engine.** Imports XMLTV, portal guides, Xtream and Jellyfin guides, matches them to channels, stores everything in UTC, and answers by the requesting device's time zone. It imports only channels that a lineup uses, and prunes past days beyond the longest catch-up window.
- **Stream hub.** Mints signed links, opens and tracks sessions, enforces per-source (and later per-account) connection limits, proxies by default, remuxes or transcodes when a device needs it, and runs ffmpeg. In the hub stage it also fails over between alternative streams.
- **Portals.** The device API for our MAG UI, the Stalker API for third-party clients, and in the hub stage M3U/XMLTV, Xtream and HDHomeRun outputs. All of them call the same services.
- **Accounts, devices and policy.** Accounts (a household starts with one), devices by MAC with an approval flow, profiles, parental rules, expiry, messages.
- **Artwork.** Fetches logos and posters, resizes them to the size a screen asks for, converts them to formats the box can show, and caches them on disk.
- **Jobs.** Source refreshes, EPG imports, library scans, cleanup, later recordings.
- **Admin.** Web UI and REST API over the same services.

### Key design decisions

1. **Stable internal ids.** Each item has an internal id that survives refreshes. Per-protocol ids (Stalker ids, Xtream ids) map to it, so clients' favorites do not break when a source refreshes. Identity across refreshes comes from a per-adapter key: provider ids for Xtream and Stalker, item ids for Jellyfin and Plex, paths for local files, and for M3U the `tvg-id`, else a normalized name and group. Items that disappear are kept as tombstones for a grace period, so a channel that comes back keeps its id and its place in favorites.
2. **UTC inside, device time zone outside.** Stalker guide days follow the `timezone` cookie.
3. **A capability profile per device.** Describes which codecs, containers, subtitle types and bitrates a device plays. The hub uses it to choose redirect, remux or transcode.
4. **Unknown requests are logged, not just rejected.** Every unimplemented Stalker action is recorded with its parameters, giving us a capture of what real boxes and clients ask for.
5. **Two device-facing APIs over one set of services.** The MAG UI talks to a small device API made for it: few round-trips, data shaped for each screen, artwork at the right size, numbers as numbers. The Stalker API is a compatibility surface with Ministra's quirks (pages of 14, numbers as strings, `cmd` strings). Both call the same services, so their behavior cannot drift, and the UI is free to need things Stalker never modelled (profiles, resume, search across everything).
6. **Accounts hold entitlements; devices belong to accounts.** Stalker clients are identified by MAC, but M3U and Xtream players log in with credentials. With accounts in the first schema (one household account by default), the hub stage adds logins, not a data migration.
7. **Mirror what is cheap, fetch the rest on demand.** Live channels, categories and guides are mirrored on refresh. Large movie catalogs, series details and anything a portal pages 14 items at a time are fetched when someone browses them and cached for a while. A refresh must never look like a crawl to a provider.
8. **Proxy by default.** Most provider URLs embed credentials or tokens, so redirecting would hand them to the device. Proxying also makes session counting exact, which connection limits depend on. Redirect is an opt-in for sources whose URLs carry no secrets.

---

## 5. Data model (first draft)

| Table | Key columns and notes |
|---|---|
| `sources` | id, type, name, host (shown in the admin), enabled, max connections, request limits, redirect allowed, refresh schedule, new categories hidden, last status |
| `source_secrets` | source id, the address and credentials, encrypted (D46) |
| `categories` | id, source id, kind (live, movie, series, radio), name, adult flag |
| `channels` | id, source id, source ref (for M3U, the stream address, encrypted: D46), identity key, name, number, logo, category, xmltv id, archive days, adult, missing since |
| `lineups` | id, name (one default lineup until the hub stage) |
| `lineup_groups` | id, lineup id, name, hidden; the categories it holds, from any source (D47) |
| `lineup_entries` | id, lineup id, number, whether the administrator pinned it, display name, group, logo override, hidden or shown, EPG override (D47) |
| `lineup_entry_streams` | lineup entry id, channel id, priority (one row per entry until merged channels arrive in M6) |
| `rules` | lineup id, position, match, action, enabled (M6) |
| `programmes` | channel id, start, end (UTC), title, description, category, archive flag |
| `library_categories`, `titles`, `episodes` | films and series, read on demand (D63): a title's kind, category, name, year, rating, poster, the source's key, what its source's list says, its details once opened, and missing since; a series' episodes by season and number |
| `library_reads` | source id, kind (films or series), when the last read worked, when it last ended, why it failed, how many titles it listed |
| `media_files` | item id, path or URL, container, codecs, duration, size, probed at |
| `accounts` | id, name, username, password hash, playlist token, lineup id, max connections, expiry, enabled, allowed outputs |
| `devices` | id, account id, MAC, label, model, default profile id, capability profile, state (pending, approved, blocked), pairing code, device secret hash, last seen |
| `profiles` | id, account id, name, hash of the PIN that switching to it asks for (D94) |
| `favorites` | profile id, lineup entry id, when added; per account until profiles came (D53, D94) |
| `progress` | profile id, title id, episode id, position, duration, finished, watched at; per account until profiles came (D65, D94) |
| `messages` | device id, text, created, delivered |
| `sessions` | stream sessions: account, device, item, source, mode, started, last seen, bytes |
| `jobs`, `job_runs` | schedule and history |
| `capture_log` | unknown requests (type, action, params, device, user agent), redacted like other logs |
| `settings`, `admin_users`, `admin_sessions`, `api_tokens` | configuration and access (D44) |

Migrations are versioned from the first schema. Schema v1 (M1) needs only sources, categories, channels, lineups and their entries, accounts, devices, sessions, capture_log and settings; the other tables arrive with the milestones that use them.

A lineup says both what an account sees of live TV and how it is presented (numbers, names, groups, logos); there are no separate packages (D45). Who may see which library of movies and series is decided in M3.

---

## 6. Source adapters

Each adapter provides: **catalog refresh**, **EPG refresh** where it has one, **play-time resolve**, and **health checks**. Adapter details below should be checked against each service's current documentation when implemented.

Each adapter also declares its **identity key** (section 4), what it **mirrors** and what it **fetches on demand**, whether its stream URLs **carry secrets** (then the hub never redirects), and its **limits**: connections, plus a request rate for API calls, with backoff on errors.

### 6.1 M3U + XMLTV

- Parse `#EXTM3U` headers (`url-tvg`/`x-tvg-url`) and `#EXTINF` attributes: `tvg-id`, `tvg-name`, `tvg-logo`, `tvg-chno`, `group-title`, `catchup*`.
- Sources can be a URL or a local file; refresh on a schedule.
- XMLTV import is streamed, not loaded whole. Match by `tvg-id` first, then by normalized name.
- Identity: `tvg-id` when present and unique, else normalized name plus group.
- Numbers (D40): playlist order by default; `numbers: provider` uses `tvg-chno`. A listed channel takes its number back from one the source no longer lists, so a replaced playlist starts again from 1.
- Plain streams: redirect or proxy. Fuzz-test the parsers.
- Movie and series entries in the layout Xtream-style providers use in their playlists (`/movie/` and `/series/` addresses) are left out of live TV.
- A fetch that fails, or a playlist that lists no channels, leaves the catalog as it was, so a provider's error page cannot empty the lineup.

### 6.2 Xtream Codes

- Login and account (`player_api.php`), live/movie/series categories and lists, per-title info, short and full EPG (`xmltv.php`), stream paths for live, movie and episode, catch-up through `tv_archive` and timeshift URLs.
- Live channels and category lists are mirrored. Per-title info (`get_vod_info`, `get_series_info`) is fetched when someone opens a title, then cached; never crawl a catalog title by title.
- Respect `max_connections` from the account. Titles and descriptions in short EPG are often Base64.
- Credentials are in every URL, so they are redacted in all logs, and Xtream streams are always proxied.
- Reference: [protocol/xtream-api-notes.md](protocol/xtream-api-notes.md), written before the adapter from open-source clients and servers and community documentation.

### 6.3 Upstream Stalker portal

Comes in M3, with movies and series (D48).

- A Go implementation of the client side of the protocol, following the protocol notes: handshake, profile, optional login, channels, genres, guide, archive, VOD, series (all three shapes), watchdog.
- Live channels, genres and guides are mirrored. Movies and series are browsed through the upstream on demand (it pages 14 items at a time) and cached.
- Uses the registered MAC and device details the user configures, never anyone else's.
- Keeps the watchdog going while a stream plays.
- Both a source (we pull from it) and a test target for the conformance suite.
- **In place:** the client (`internal/adapters/stalker`), tested against a fake portal written from the notes, and portals as sources of live channels and guides: one session per source shared by its reads and its streams, links asked for as each stream starts, the box's own stream ended first at the source's limit, and the keep-alive while a stream plays (D76). Films and series are read a page at a time as devices browse them, each category a shelf of its own, in all three series shapes (D77).

### 6.4 Jellyfin

- First of the library adapters: its API is open and documented, and a Jellyfin container with generated clips makes adapter tests easy.
- Authenticate with an API key or user token; list libraries, movies, series, seasons, episodes, artwork; read playback info; optionally read Live TV channels and the guide. **In place:** an API key, with an optional user whose libraries boxes see; films, home videos, series and episodes with their artwork (D66). Live TV from Jellyfin is not read.
- Play by direct stream when the device can handle the file, otherwise remux or transcode. For full video transcodes, prefer Jellyfin's own transcoder (it may have hardware acceleration and it spares the LXC's CPU); Overscan's ffmpeg handles remuxes and audio-only transcodes. Decision made by the hub, not the adapter. **In place (D64, D68, D71):** the file as it is, or through Overscan's ffmpeg when the box cannot play its format or its sound; and, when the box cannot play its video, Jellyfin's own transcode to H.264 in MPEG-TS, with one sound track at a time, which the hub passes on, sought in and switched by asking for it again.
- Sync watch state back to Jellyfin as a later option.

### 6.5 Plex

- Authenticate with a Plex token; list library sections and items, artwork and media parts; stream the part or request a transcode. **In place:** a token, pasted in the admin; libraries of films and of shows, each a shelf, with their titles, details, episodes and pictures, the pictures fetched with the token that their kept addresses leave out; each film's or episode's file played as it is through the hub, and through Overscan's ffmpeg when the box cannot play its format or its sound; subtitle files beside a video (D72).
- The same transcoding preference as Jellyfin: Plex's transcoder for full video transcodes. **Not yet:** Overscan does not ask a Plex server to transcode, so video that the box cannot play from Plex goes through Overscan's own ffmpeg where the administrator lets it (D73), and otherwise plays as it is.
- Plex has documented its API since September 2025 ([plex-api-notes.md](protocol/plex-api-notes.md)); isolate it behind the adapter, and test against a real server, which has not been done yet.

### 6.6 Local files

- Watch configured root folders (allowlist, no escaping through symlinks).
- Probe with ffprobe, cache results.
- Identify titles from filename conventions and NFO sidecar files. Optional TMDB metadata with the user's own API key came in M4 (D88; TMDB's terms and attribution in `protocol/tmdb-notes.md`).
- Group episodes into seasons and series.
- **In place (D67, `library-folders.md`):** a folder is a source, opened so that nothing outside it can be read; it is read on demand and again after a quarter of an hour rather than watched; titles come from names, `.nfo` files and pictures; episodes group into seasons and series. ffprobe reads a file the first time it plays, and again when it has changed (D68).

### 6.7 Later

- Tvheadend, HDHomeRun, Enigma2, DVBLink and DVBViewer. Their public APIs and existing open-source clients are worth reading before porting.
- Dispatcharr, m3u-editor and similar tools need no adapter of their own: they publish M3U, XMLTV and Xtream, which the existing adapters read (principle 8).

---

## 7. Playback pipeline

### Modes

| Mode | When | Cost |
|---|---|---|
| **Proxy** (default) | Always, unless a source allows redirect. Keeps upstream URLs, credentials and tokens off devices, adds whatever user agent, header or token an upstream needs, and makes session counts exact. | Bandwidth through the server |
| **HLS relay** | The device asked for a continuous stream and the source answers with HLS: the hub follows the playlist and sends the segments as one MPEG-TS stream (D42). | Like proxy |
| **Redirect** | Opt-in per source, for URLs that carry no secrets, when the device can play the URL directly. Sessions are then estimated from the watchdog. | None |
| **Remux** | Right codecs, wrong container or protocol (e.g. MKV to TS, or to HLS), or a live stream whose timing is broken (*repair*, a switch per channel). `ffmpeg -c copy`. | Low CPU |
| **Remux + audio transcode** | The video plays but the audio does not (e.g. DTS or TrueHD). Copy the video, transcode the audio. | Low CPU |
| **Transcode** | The device cannot play the codec, resolution or bitrate, or image subtitles must be burned in. For library items, prefer the media server's own transcoder. | High; hardware acceleration optional |

### Links and sessions

- `create_link` returns an Overscan URL carrying a signed token (HMAC, bound to device and item). The token only needs to be valid when playback starts (about a minute). The session it opens carries reconnects, seeks, range requests and HLS segments for as long as the device keeps playing, so a short token never cuts off a long film.
- The hub resolves the real upstream address only when the URL is requested.
- Sessions are counted per source against its connection limit (and, from M6, per account against the account's limit). They close when the client disconnects, on idle timeout, or on watchdog silence for redirected streams.
- VOD supports HTTP range requests for seeking when proxied. Remuxed or transcoded VOD has no byte positions to seek in: restart ffmpeg at the new position first (simple), and add HLS VOD playlists cut at keyframes only if that proves too slow.
- "Shared tuner" mode (off by default): several local viewers of the same live channel share one upstream connection. **In place (D93):** a channel's tuner plays it once into a buffer on disk, which every box, Stalker client and recording on the channel reads.
- A stretch that a source sends again, after a restart on its side, is left out of every continuous stream, so a player gets each moment once (D43).
- Failover (M6): a merged lineup channel tries its next stream when one fails or its source is at its limit. A backup stream picked by hand for a channel can come earlier, for a household with a second provider.

### Device capability profile

Starts from the datasheet-level facts for the MAG322w1 (HEVC and H.264, a range of audio codecs including Opus after the 2019 firmware, SRT and text MKV subtitles) and is then **measured**: a probe page served by Overscan runs on the box, tries test streams through the player API, and reports what actually works. Profiles are editable and stored per device. **In place (D68, D70):** the datasheet's profile, which a box starts from; and the playback check, on the box's *Settings*, which plays 23 clips that ffmpeg makes on the server, one for each sound codec, video codec, 10-bit or 4K picture and file format that films have, and asks the viewer after each whether it plays, since the box's player reports a start even when it cannot decode the sound. The server keeps what each box's check found, and a box never checked plays as the last box of its model that was. The admin lists what each box plays and can forget it, so that checking again is how a profile changes; editing one by hand waits until a box needs it. What the box's player says of each clip is kept too, to learn whether a later check can read some answers from it.

Expect most H.264 and HEVC files to direct-play. Audio codecs and image-based subtitles are the likely trouble, so budget for remuxes and audio transcodes rather than video transcodes.

### Subtitles, audio tracks, resume

- External subtitles delivered as SRT or WebVTT.
- Image-based subtitles (PGS, VobSub) that the box cannot render need burning in, which means a full video transcode; offered per title, off by default.
- Audio and subtitle track selection through the player API.
- Resume positions stored per profile and updated while playing.
- **In place (D65, D68, D69):** text subtitles beside a file in a folder, and those that Jellyfin gives, reach the box as SRT through the hub, moved earlier for a stream through ffmpeg that starts part way through; the box shows the text subtitles in a file it plays as it is itself; a converted stream carries every sound track, and the box picks among them; resume per account, and per profile since M5 (D94).

### Artwork

Logos and posters go through Overscan: fetched once, resized to what the screen asks for, converted to JPEG or PNG, and cached on disk with a size cap. The MAG322w1 also shows WebP and SVG, but JPEG and PNG stay the default until decode times are measured on the box. Devices never fetch artwork from upstream, which also keeps media-server tokens (Plex artwork URLs carry one) off devices.

---

## 8. Stalker API server

The contract is [`protocol/stalker-protocol-notes.md`](protocol/stalker-protocol-notes.md). Implementation notes:

- **Endpoints:** `/portal.php`, `/stalker_portal/server/load.php`, `/server/load.php` under any base, and the UI at `/c/`.
- **Identity:** devices keyed by the `mac` cookie. An unknown MAC returns 401 and appears in the admin's pending devices, where one click approves it; auto-registration stays an admin toggle, off by default. Optional per-device login through `do_auth`, checked against the device's account. Blocked devices get a profile with `blocked=1`.
- **Tokens:** a new handshake issues a new token and invalidates the old one. Missing or stale tokens return `Authorization failed.` or 401.
- **Shapes:** numbers as strings, empty lists as empty arrays, paging of 14 items, `{"js": ...}` wrapper. Unimplemented actions return 404 so clients fall back.
- **Modules:** `stb`, `itv`, `epg`, `tv_archive`, `vod`, `series`, `account_info`, `watchdog`.
- **Films and series:** films in `vod` and series in a `series` module of their own, a series' seasons read from its source when a client opens it (D74), and every film and episode played as it is, with range requests (D75). The inline shape on `vod`'s lists is not served: it would have Overscan read every listed series from its source as a client scrolls (D22). The upstream adapter reads all three shapes (6.3).
- **Messages:** the watchdog `get_events` call is the channel for messages to a box. Our own MAG UI gets them through the device API.
- **Capture log:** unknown actions and parameters are stored, redacted, for later analysis.
- **Compatibility matrix:** tracked per client and version in `docs/compat.md`.

The protocol notes come from a client that has only been tested against fakes, so treat them as a strong hypothesis, and fix them where a real client or box disagrees.

---

## 9. MAG portal UI

### Constraints

- The MAG322w1's browser (WebKit 538.1 on firmware 2.20.14 and 2.20.16) runs ES5 and nothing newer. Flexbox, transforms and animations need the `-webkit-` prefix, there is no grid, and there is no `localStorage`. The CPU is weak (750 MHz dual-core MIPS) and memory small (512 MB RAM). The user agent that Stalker clients send for a MAG box (WebKit 533.3) imitates an older model, so it says nothing about this browser.
- Input is a remote control through key events. Everything must work with D-pad, OK, Back and number keys.
- Playback, device identity, storage and windows come from the box's JavaScript API (`gSTB` and friends), which Infomir documents publicly.
- MAG emulators such as StbEmu (which can emulate a MAG322) run the same page in a modern WebView. They are handy for functional tests and tell us nothing about performance or the old engine.

### Probe first (done)

The first M1 task was a probe page on the real box, because its answers would decide the toolchain. It is a static page and a small endpoint that records what the page reports: the real user agent, missing ES5 built-ins, newer JavaScript and DOM features, CSS support, storage, XHR, image formats, the `gSTB` API and its version, the viewport and the output mode. Its results from the MAG322w1 settled the toolchain below (D32). A later version of the page measures playback capabilities (section 7).

### Device API (decided)

- JSON under `/api/device/v1/`, built for the box: one request per screen where possible, server-side paging sized to the UI, artwork URLs at the size each screen draws, numbers as numbers. Documented in [`api/device.md`](api/device.md).
- **Version 0** (M1) has a session, the channel list and play links. It knew a box by its MAC address alone until pairing (D37, D39).
- **Pairing** (D39). On first load the page sends the MAC and model from the JavaScript API. An unknown box gets a secret to keep and shows a six-digit code, and the administrator approves it by the code (`overscan devices approve`, later under Devices → Pending). Approval links the box to an account and pairs it with the secret, so a copied MAC alone is not enough; the Stalker API refuses a paired box's MAC. A box that loses its secret pairs again. The box has no `localStorage` and its `stbStorage` does not survive a restart, so the page keeps the secret in a cookie, with a copy in a file saved with `gSTB.SaveUserData` (D38).

### Resolution (decided)

- One layout, not separate skins. Design on a 1280×720 reference grid and size everything in `rem` units from a root font size computed at boot from the viewport. No CSS transforms, which are costly on this hardware.
- Request artwork from the server at the matching size.
- The box draws pages at its graphics resolution, a setting separate from the HDMI output mode, and scales them up to the output: with 1280×720 graphics and 1080p output, pages get 1280×720. The MAG322w1 offers no graphics resolution above 1280×720, so it is measured there. A box that offers 1920×1080 gets the same layout, measured when one is tested; if it is sluggish there, the docs recommend 1280×720 (a setting on the box, no code).
- 576 and 480 (SD) modes are out of scope for v1.

### Themes (decided, D41)

- The one layout wears a theme: colours, fonts, background, logo and the look of the focus. Overscan ships its own theme, and the administrator can install others.
- A theme is a small file of named values, and the server turns it into a stylesheet that loads after the page's own. The box's browser matches Safari 7 and 8, which predate CSS custom properties, so the browser cannot do the substitution itself; the probe can confirm. An advanced theme can add a stylesheet of its own, with no promise that it keeps working when the page changes.
- Themes change the look, not the layout or the behaviour, so there is still one layout to build and test.
- No theme that imitates another product's portal ships with Overscan or enters the repository (clean-room policy).

### Tooling (decided; settled by the probe, D32)

- **TypeScript for types, a separate compiler for ES5.** TypeScript 6.0 deprecated `target: es5` and `--outFile`, and TypeScript 7.0, the Go-native compiler, removed them ([TypeScript #62196](https://github.com/microsoft/TypeScript/issues/62196)). Pinning TypeScript 6 would work for now, but it is the last release of the JavaScript-based compiler, so it is a dead end. Instead:
  - type-check with `tsc --noEmit`, using the `es5` and `dom` libraries (TypeScript keeps both) so that newer built-ins such as `Map`, `Set` and `Promise` are errors unless we ship our own tiny implementation;
  - emit ES5 with Babel or SWC, bundle with a bundler whose own glue code is ES5 (Rollup has a setting for this), and minify with terser at `ecma: 5`;
  - keep the guard: parse the final bundle with an ES5-only parser (for example acorn at `ecmaVersion: 5`) and fail the build on anything newer;
  - avoid syntax whose ES5 output needs newer built-ins: `for…of` over anything but arrays, spreading iterables, generators, `async`/`await`;
  - lint against what those libraries declare but the box lacks: `fetch`, `URLSearchParams`, `TextDecoder`, `Intl`, `localStorage` and XHR's `json` response type;
  - no polyfills: the box has every ES5 built-in, and the newer ones it lacks are already type errors.
- **CSS through Autoprefixer**, since flexbox, transforms and animations need the `-webkit-` prefix on the box. Its browser target is Safari 7, which the probe results match.
- No UI framework; a tiny view and focus-navigation layer. Hand-written ES5 stays the fallback for any hot path that proves too slow on the box.
- A browser dev mode with a `gSTB` shim and keyboard mapping, plus testing on the real box. StbEmu with a MAG322 profile covers functional checks.
- Review Infomir's example mini-portal and community frameworks for ideas, subject to their licences.
- **Updates must reach the box:** versioned asset names, a no-cache entry page, and a "reload portal" action in Settings, since set-top browsers cache aggressively.

### Screens

| Screen | Notes |
|---|---|
| Boot and identity | Reads the MAC and model from the box, shows a pairing code if the box is not approved yet, and shows errors with a clear message. |
| Home | A row of large tiles, Live TV first, then Movies, Series and the rest, as many TV home screens have; continue watching, favorites and recent below them. Our own design: the clean-room policy rules out copying the look of a portal skin. |
| Live TV | Channel list with now/next, zapping, number entry, last channel, group switching. |
| Guide | Time grid, past days where catch-up exists. |
| Movies, Series | Categories, details, resume, audio and subtitle selection. |
| Search | On-screen keyboard. |
| Favorites | Per profile. |
| Settings | Language, time zone, PIN, diagnostics (version, last request, memory), reload portal. |
| Messages | From the admin. |
| Later | Recordings, radio (both in M4, D87 and D89). |

Performance rules: small DOM, no layout thrash, lazy images at small sizes (thumbnails produced by the server), memory checks in the diagnostics screen. Targets to validate in M1: a visible response to a key press within about 150 ms, and the channel list on screen within about 3 s of the page loading.

---

## 10. Admin UI and REST API

### Pages

Dashboard (health, active streams, devices) · Sources (add, test, refresh, errors) · Lineup (reorder, rename, hide, regroup, EPG mapping with fuzzy match and manual fix; rules, merges and several lineups from M6) · Libraries · Devices (pending approvals, label, account, profile, block, message, last requests) · Accounts (one household account until M6, then logins and limits) · Activity (who is watching what) · Profiles · Jobs · Logs · Settings · Admin users.

Until the admin exists (M2), sources are declared in the config file and devices are approved with a small CLI. From M2 the admin manages both; a source in the config file is imported once and then edited in the admin (D46).

### Tech

- Server-rendered Go templates with htmx and a little JavaScript, embedded in the binary.
- REST API first, documented with OpenAPI. API tokens for automation.
- Admin sign-in (D44): one administrator, with an argon2id password hash set on the server with `overscan admin password`, which also resets a forgotten one. Sessions are kept in the database; forms and API writes carry CSRF tokens; failed sign-ins are slowed down. The admin shares the portal's address unless `admin_listen` names another. Optional reverse-proxy header auth later.

---

## 11. Security and privacy

| Area | Approach |
|---|---|
| Exposure | LAN-only by default. Documentation advises against exposing it to the internet; if needed, behind a reverse proxy with TLS. |
| Device identity | A MAC is weak identity. New devices wait for approval in the admin, and the MAG UI also holds a device secret issued at approval. Optional per-device login for Stalker clients; auto-registration off by default. |
| Admin | An argon2id password hash set from the command line, sessions in the database behind HttpOnly and SameSite=Strict cookies, CSRF tokens, slowed-down failed sign-ins, and an optional address of its own (D44). |
| Secrets at rest | Source addresses and credentials, and the channels' stream addresses that carry them, encrypted with AES-256-GCM under a key file kept outside the database, readable by its owner only (D46). |
| Logs | Redaction of credentials, tokens and hardware ids (serials, signatures) by default, the capture log included. MAC addresses appear in the log, which stays on the server, so that devices can be approved from it; the support bundle keeps only their maker's half. |
| Stream tokens | HMAC-signed, bound to device and item, valid for about a minute to start a session; sessions end on disconnect or idle. |
| Outputs (M6) | M3U and Xtream clients send credentials in URLs by design, so outputs are LAN-only by default. Playlist tokens can be rotated; per-account connection limits. |
| Artwork | Fetched and served by Overscan, so upstream hosts and media-server tokens never reach devices. |
| SSRF | Overscan only fetches configured source URLs and the upstream addresses they resolve to. Optionally block private and link-local ranges for sources that should be public. |
| Local files | Allowlisted roots, symlink handling, no path traversal. |
| ffmpeg | Argument arrays only (no shell), protocol allowlist, resource limits, sandboxing via systemd where possible. |
| Parsers | Fuzz M3U, XMLTV and JSON parsers. Size and time limits. |
| Dependencies | Minimal, pinned, scanned in CI. |
| Telemetry | None. |

---

## 12. Tech stack (proposed)

- **Language:** Go.
- **Module path:** `overscan.gureet.ca`, a vanity import path. The subdomain serves a `go-import` meta tag over HTTPS that points at the repository, from its `gh-pages` branch (D80).
- **HTTP:** standard library `net/http` with a small router.
- **Storage:** SQLite in WAL mode through ncruces/go-sqlite3, a pure-Go driver (no cgo; D34), with versioned migrations embedded in the binary and queries written by hand.
- **Media:** `ffmpeg` and `ffprobe` as subprocesses; VAAPI optional.
- **Admin:** Go templates + htmx, embedded with `go:embed`.
- **MAG UI build:** TypeScript for type checks, Babel or SWC for ES5 output, an ES5-output bundler, terser, an ES5 parse guard and Autoprefixer, with no polyfills (section 9).
- **Logging and metrics:** `log/slog`, Prometheus-format `/metrics`, `/healthz`.
- **Config:** YAML file plus environment overrides for infrastructure (addresses, paths, key file, log level). Everything the admin edits lives in the database. Sources declared in YAML are imported once, at start, and from then on edited in the admin (D46).
- **Release:** GoReleaser for `linux/amd64` and `linux/arm64`, a Docker image, a `.deb`, and a Proxmox LXC setup script.
- **CI:** GitHub Actions, kept lean while the repository is private, because free minutes run out quickly.
- **Keeping a Rust option open:** core logic behind interfaces, a documented schema, black-box conformance tests, UI and admin assets separate from the server. These seams pay for themselves anyway (tests, other implementers); add an interface where there is a second implementation or a test double, not by default.

---

## 13. Repository layout

```
overscan/
├─ README.md  LICENSE  CONTRIBUTING.md  CODE_OF_CONDUCT.md  SECURITY.md
├─ docs/
│  ├─ plan.md  protocol/  api/  devices/  decisions.md  clean-room.md  testing.md  compat.md
│  └─ images/  logo/
├─ cmd/overscan/
├─ internal/
│  ├─ adapters/{m3u,xtream,stalker,jellyfin,plex,local}
│  ├─ catalog/  epg/  lineup/  hub/  media/  artwork/
│  ├─ portal/{device,stalker,m3u,xtream,hdhr}
│  ├─ accounts/  devices/  admin/  jobs/  store/  config/
├─ web/{probe,mag,admin}/
├─ conformance/{stalker,xtream}/   black-box suites and fictional fixtures
├─ deploy/{systemd,docker,lxc}/
└─ tools/
```

---

## 14. Milestones

Sizes are relative effort: S small, M medium, L large.

Every milestone ends with a short hardening pass: the security checklist (section 11) applied to what is new, docs, packaging, and from v0.1 on an upgrade test from the previous release.

### M0: Foundation (S), done
Repository, README, licence, docs skeleton, CI skeleton, `docs/clean-room.md`, `docs/decisions.md`, logo. **No code.**
*Done when:* the first commit is on `main` and the plan and protocol notes are in `docs/`.

### M1: Hello MAG (M)
Order matters: the probe page comes first, because the box's real browser decides the UI toolchain. Most of the rest needs no hardware.
- Probe page on the MAG322w1 (section 9). **Done.**
- Go module, config, SQLite, migrations; schema v1 with one household account and a default lineup. **Done:** the store (D34), with the household account and the default lineup in schema version 1; later M1 work adds its tables in new migrations.
- Stalker API: handshake, profile, `do_auth`, genres, channels, `create_link`, `watchdog`, `account_info`; unknown actions return 404 and go to the capture log. **In place:** the handshake, profile, `do_auth`, `account_info`, `watchdog`, genres, channels, `create_link`, 404s and the capture log ([protocol notes](protocol/stalker-protocol-notes.md), section 11).
- Device registry with pending approvals (approved from the CLI until the admin exists). **In place:** the registry (schema version 2) and the `overscan devices` commands to list, approve, block and remove devices; the Stalker API adds unknown devices as pending.
- One M3U source, declared in the config file. **In place:** M3U sources in the configuration file, read at start and on a schedule, with stable channel IDs, missing channels kept for 7 days, and new channels numbered into the default lineup (D35), in playlist order unless a source asks for its own numbers (D40).
- Stream hub, minimal: signed links, sessions, proxy mode, the source's connection limit. **In place:** links signed with a key held in memory, sessions that carry reconnects and HLS segments, proxying of continuous streams and HLS (playlists rewritten so every address goes through the hub), HLS relayed as MPEG-TS to a player that asked for a continuous stream (D42), stretches that a source sends again left out (D43), and per-source limits (D36).
- Device API v0 and a minimal MAG page: list channels and play one. **In place:** the device API ([`api/device.md`](api/device.md)), with sessions, the channel list and play links; boxes are known by MAC address until pairing (D37). The first MAG page ([`web/mag/`](../web/mag/README.md)), served at `/c/` and `/stalker_portal/c/`, waits for approval, lists the channels by group and plays them through the hub; it plays channels with picture and sound on the MAG322w1.
- Pairing: the MAG page keeps a device secret, so that a copied MAC address alone is not enough (section 9). **In place:** D39; the box shows a code, approved with `overscan devices approve <code>`, and the Stalker API refuses a paired box's MAC address.
- An admin-only debug playlist of the lineup, behind the admin token, to test the hub in VLC before the MAG page exists (not the M6 output). **In place:** `overscan debug playlist` prints its address; the token lives in the data directory. [`trying-it.md`](trying-it.md) says how to try what works so far.
- Conformance suite harness with the first scenarios; the reference client connects. **In place, the reference client still to connect:** [`conformance/stalker`](../conformance/README.md) runs Overscan with a fictional lineup and checks sign-in, unknown and blocked boxes, tokens, genres, channel lists, links and the keep-alive call over HTTP.
- UI toolchain settled from the probe results. **Done:** D32 and D33.
- LXC install script and systemd unit, since the first target is an LXC. **In place:** `install.sh` and a sandboxed `overscan.service` in the release archives, and [`install-lxc.md`](install-lxc.md). ffmpeg joined the install with stream repair (D57).
- Soak-test baseline. **In place:** `tools/soak` and a weekly ten-minute run in CI ([`testing.md`](testing.md)), with a first baseline; the 24-hour run on the LXC is still to do.
*Done when:* the MAG322w1 plays a channel from an M3U source, the reference client lists the same channels, the probe results are written up, and the soak test records RAM and CPU.
*Status:* the MAG322w1 plays channels from an M3U source, with pairing and playlist numbering (v0.0.6); the probe results are written up and the soak baseline recorded. The reference client still has to connect.

### M2: Live TV (L), ending in v0.1
- XMLTV and portal guides, matching, now/next and day guides; only lineup channels imported, past days pruned by catch-up window. **In place:** each source's guide read into the database, an Xtream server's or the one a playlist names, matched by guide ID and then by name (D51); Stalker clients get now, next and day guides, and the device API has the calls that the new MAG page will use.
- The Xtream adapter for live TV, with request limits and backoff. The upstream Stalker adapter moves to M3 (D48). **In place:** Xtream sources in the admin and the configuration file, read through the API one request at a time, with the account's limit of streams kept, stream addresses made at play time, and a playlist source able to become an Xtream one with its channels kept (D50).
- Lineup basics: hide or include channels, one at a time or a whole category at once, renumber, rename, regroup. Large provider lists are unusable on a box without them. Edits survive refreshes, and numbers set by the administrator are pinned (D47). **In place:** the admin's Lineup page and its API.
- Stream repair: a switch per channel for streams that stutter, skip or replay, which passes the stream through ffmpeg with `-c copy` to rebuild its timestamps without re-encoding (section 7). ffmpeg joins the install. **In place:** the switch on the Lineup page and in its API; ffmpeg reads the stream from a pipe after the repeat filter and writes it to another, and a stream that it cannot read plays as it is (D57). `install.sh` installs ffmpeg, and `overscan debug channel -repair on` or `off` compares a channel with and without repair. *Repair every channel* makes repair the default, which each channel can follow or override (D58).
- Categories, favorites, numbers, parental PIN. **In place:** favourites per account and a recent list per box (D53), and the parental PIN with locked groups and channels (D55).
- Artwork proxy for channel logos. **In place:** Overscan keeps its own copies of logos, scaled down (D54).
- Admin v0: sign-in (D44), sources in the database with their secrets encrypted (D46), lineup, devices with pending approvals, the household account, capture log. **In place:** all of these pages and the JSON API ([`api/admin.yaml`](api/admin.yaml)), with an overview of what is playing; the household account is the only one and has no page of its own yet.
- MAG live TV and guide screens, and the home screen, built on the theme layer with Overscan's own theme (section 9). **In place:** Home in two looks, Live TV with a small player, the info banner, the guide, search and settings, in Overscan's own theme (D52); the theme layer for other themes waits for M5 (D41).
- **v0.1.0:** packaging (LXC script, Docker), backups, the redacted support bundle, a security pass over everything so far. A private release: the repository stays private for now (D49). **In place:** `overscan backup`, a consistent copy of the database while the server runs, with [`backup.md`](backup.md) on what to keep and how to restore it; Overscan's files are readable by their owner only, whatever the umask. The admin offers the support bundle, now on its *Settings* page (D59): the version and settings, the state of sources, lineup and devices, what is playing, the capture log and the log's last 1,000 lines, without addresses, logins or more than a MAC address's maker. Each release archive holds a `Dockerfile` and a `compose.yaml` ([`install-docker.md`](install-docker.md)), which CI builds and checks. The security pass is written up in [`security.md`](security.md): the model, what is in place, the known limits, and what the review changed, with fuzz tests for HLS playlists, the stream analyser and the repeat filter.
*Done when:* a full Xtream lineup with guide works on the box and in the reference client, the admin can fix numbering and grouping, and v0.1.0 is tagged.
*Status:* the first phase, admin v0 with lineup editing, is in place (v0.0.10 to v0.0.12), and so are Xtream sources and the guide (v0.0.13 to v0.0.15). The new MAG interface passed its test on the box (v0.0.16 and v0.0.17). Stream repair and the v0.1.0 packaging are in place (v0.0.18 onwards); v0.1.0 waits for its test on the box and in the reference client.

### M3: Libraries (L)
- The admin's redesign (D59): the TV interface's look, a rail on the left and tabs on a phone, and a *Settings* page; then the Overview, Lineup, Sources and Devices pages in that design. **In place:** the look, the rail, the tabs and the *Settings* page; the Overview, with a *To do* list of what needs the administrator, such as a box waiting with its code to approve; the Lineup, where a channel opens in a panel beside the list, ticked channels bring up a bar of actions, and a group's actions sit in its head; Sources, each with its state and its setting for new categories, and a choice of type when adding one; Devices, with pairing by code first. The Lineup page is now *Live TV*, beside *Movies* and *Series*, which hide, rename and order the shelves of films and series and hide titles (D79).
- Live TV after v0.1.0-rc.2, from tests on the box. **In place:** while the focus moves through Live TV's groups, a group's channels show first as text, and a box can choose to open groups with OK only (D62); a stream that starts part way through a packet is read from its first whole packet (D60); and repair can reach channels whose address is HLS, behind a setting of its own (D61).
- Jellyfin, then local files, then Plex; metadata and artwork. **In place:** Jellyfin servers as sources of films and series, read with an API key, as one user or for every library, each library a shelf, read again after a quarter of an hour, and their files played as they are through the hub (D66); and folders on the server, read by the names, `.nfo` files and pictures that Kodi, Jellyfin and Plex use, each folder a shelf, their files played by the hub itself (D67); and Plex servers as sources of films and series, read with a token, each library a shelf, their files played as they are through the hub (D72).
- The upstream Stalker adapter (section 6.3, D48): live channels, genres and guides mirrored, movies and series browsed on demand. **In place:** portals as sources of live channels and guides, read as the box that the provider registered, with links asked for at play time and the keep-alive while a stream plays (D76), and their films and series read a page at a time as devices browse them (D77).
- Movies and series from Xtream and upstream Stalker sources, browsed on demand with caching. **In place:** Xtream sources' films and series, each list read in one request when a box first asks for it, details and episodes when a title is opened (D63); the admin's *Sources* page says what the reads found. A Stalker portal's are read a page at a time as boxes and clients browse them (D77).
- Movies and series in the device API, the MAG UI and the Stalker API (inline series shape first); resume; subtitles and audio tracks. **In place:** the device API, and the MAG page's *Movies* and *Series*: shelves, details, episodes, playing with seeking and pausing, the next episode, and resume, with progress kept per account (D65); sound tracks named as the server knows them (D68), and subtitles from beside the file, from Jellyfin, or in the file itself (D69); and the Stalker API's films in `vod` and series in a `series` module, a series' seasons read when a client opens it rather than inline (D74), each played as it is (D75).
- Playback: direct play through the proxy with range requests, then remux and audio-only transcode, seeking by restarting ffmpeg, and full transcodes last (preferring the media server's transcoder). **In place:** direct play through the hub, which passes on range requests (D64); a file whose format or sound the box cannot play goes through ffmpeg, which copies its video into MPEG-TS and converts the sound to Dolby Digital, and a seek starts ffmpeg again at a picture (D68); video that the box cannot play, from a Jellyfin source, goes through Jellyfin's transcoder (D71); and, where the administrator lets it, video that the box cannot play from any other source goes through Overscan's own ffmpeg, which makes H.264 of it at up to 1080p, mapping a high dynamic range to the standard one, for up to three titles at a time (D73). Each of the three is a setting of its own: the conversion and Jellyfin's transcoding on at first, Overscan's own transcoding off (D78).
- Capability profiles measured by the probe page. **In place:** the playback check, from the box's *Settings*, kept per device and used to decide which films go through ffmpeg (D70).
*Done when:* a movie and an episode from each source play on the box with seek and resume, and unsupported formats are remuxed or transcoded automatically.
*Status:* done, and released with M4 in v0.3.0, on 19 November 2026; there was no v0.2.0 release. Its pre-releases: the admin's redesign and the Live TV follow-ups (v0.2.0-alpha.1 to alpha.4), Xtream films and series on the MAG page (alpha.5), Jellyfin sources (alpha.6), folders (alpha.7), the sound of films that the box cannot play converted, with subtitles (alpha.8), the playback check that measures what each box plays (alpha.9), Jellyfin's transcodes of the video a box cannot play (alpha.10), Plex sources (alpha.11), transcodes with Overscan's own ffmpeg (alpha.12), the Stalker API's films and series (alpha.13), the upstream Stalker source (alpha.14), and fixes to playing films and series on the box (alpha.15).

### M4: Catch-up, recording, parental control (M)
Each phase ends with a pre-release of v0.3.0 (`v0.3.0-alpha.N`), as M3's phases did with v0.2.0.
- Archive passthrough from sources that have it: Xtream servers, playlists that describe their catch-up, and Stalker portals, on the box and for Stalker clients. **In place:** a channel whose source keeps an archive plays its past programmes from it, through the hub, with the source's login kept on the server; the box's guide marks them and goes back as far as the archive, and the page seeks by asking again from a time (D81); a playlist's channels play their archives as its catch-up attributes say (D82, [catch-up notes](protocol/catchup-notes.md)); a Stalker portal's play at the link that the portal gives for the programme, which the box's player seeks in with byte ranges (D84); Stalker clients see the archive flags and the programmes marked, and play them with `tv_archive/create_link`; and a read of a guide keeps the past that the read before had (D83).
- Full parental control, adult marks: the PIN for films and series as for channels (D55, D79), and the marks that sources give. **In place:** shelves of films and series, and titles, are locked from the *Movies* and *Series* pages; the box marks a locked title, hides its picture and its details until the PIN is typed, and plays it only with the PIN; Stalker clients do not see locked titles (D85). Xtream servers', Stalker portals' and playlists' marks for adults are kept, and lock, hide or do nothing, as a setting says, locking at first (D86, D91).
- Radio. **In place:** the radio stations that Xtream servers and playlists list are marked in the lineup; the box plays them on a *Radio* screen, without a picture, and a setting keeps them out of its Live TV (D87).
- Optional TMDB metadata for local files, with the administrator's own key. **In place:** with a key given on the *Settings* page, the films and series in folders that lack a poster or a plot get what their folders leave out from TMDB, found by the TMDB ID in their names or `.nfo` files or by name and year, looked up in the background one request at a time; a series' episodes when a box opens it (D88).
- Local recording and timeshift (optional). **In place:** recordings made on the server, off until the administrator gives them room: a box records a programme from its guide, the server plays the channel through the hub as a device of its own, within its source's limit, and writes it to a folder that a folder source reads, so that boxes play it on *Movies*; the admin lists, calls off and deletes them (D89). Timeshift, behind a setting that starts off: the server keeps the last minutes of the channel a box watches in a buffer on disk, while it watches, so that the box can pause live TV and go back in it (D92).
*Done when:* a past programme plays on the box, with seeking, from each kind of source that keeps catch-up (an Xtream server, a playlist, a Stalker portal), and a Stalker client sees and plays the same; a recording made on the server plays back on the box; a locked or adult film asks for the PIN on the box and is hidden from Stalker clients; a radio station plays; and a folder's titles get TMDB details once a key is set.
*Status:* done, and released with M3 in v0.3.0, on 19 November 2026. Its pre-releases: catch-up from Xtream servers and playlists (v0.3.0-alpha.1) and from Stalker portals (alpha.2), locks on films and series with sources' marks for adults (alpha.3), radio (alpha.4), TMDB for folders (alpha.5), recordings (alpha.6), the milestone's hardening (alpha.7), and timeshift (rc.1, D92). The *Done when* was checked against fake panels, a fake portal and TMDB, the conformance suite as a Stalker client, and the MAG page in a desktop browser with a stand-in for the box's player. Most of it has still to be tried on a MAG box and with real providers.

### M5: Households (M)
Each phase ends with a pre-release of v0.4.0 (`v0.4.0-alpha.N`), as M4's did with v0.3.0. In this order:
- Shared-tuner mode (section 20, open question 1), off by default: one connection to a source serves every local viewer of a channel, whether boxes, Stalker clients or recordings, and counts once against the source's limit. Sharing a connection between screens is between the subscriber and their provider (section 2). **In place:** a setting on the *Settings* page that starts off; while it is on, a channel's tuner plays it from its source into a buffer on disk, as timeshift's does, and every screen on the channel reads the buffer, counting once against the source's limit, so that a box can record the channel it plays on a source that allows one stream (D93).
- Several profiles per account, switchable on the box, each with its own favourites and resume points. A household that adds no profile sees no change. **In place:** the admin's *Devices* page adds, renames and removes profiles, gives one a PIN, and chooses the profile each device plays as; once there are two, a box shows the one watching on Home and switches with the yellow key or from *Settings*, asking for the profile's PIN if it has one. The account's first profile took its favourites and resume points; recent channels, the box's own settings and the parental PIN stay as they were (D94).
- Expiry: the administrator sets the account's end date; past it, boxes and Stalker clients play nothing. **In place:** the *Settings* page's *Household* card sets the last day the household's account plays; past it, the box says that the account has ended, with the date, and plays nothing, Stalker clients are refused as blocked devices are but still read the date, and the account's recordings stop (D95).
- Messages and events: the administrator sends a message to one box or to every box, which shows it once. **In place:** the admin's *Devices* page sends a message to one box or to every box that runs Overscan's page; a box shows it within five minutes, over whatever is on screen, until the viewer closes it, and the admin sees which boxes read it. Stalker clients get none, and no other events are sent, until an allowed source says how a portal sends them (D96).
- Installable themes (D41): the administrator adds a theme in the admin, for the household or for a profile.
- Compatibility matrix for third-party Stalker clients (OTT Navigator, TiviMate) and MAG emulators (StbEmu), from tests with those apps.
- The milestone's hardening: the security checklist on what is new (text and themes from the admin shown on boxes above all), docs, and an upgrade test from v0.3.0.
*Done when:* two profiles on one box keep their own favourites and resume points, and the box switches between them; an account past its end date is refused on the box and in Stalker clients, which see the date; a message from the admin shows on the box; a theme installed in the admin changes the box's look for the household or for one profile; with the shared tuner on, two boxes on one channel, or a box and a recording of that channel, use one connection to the source; and a compatibility matrix says what OTT Navigator, TiviMate and StbEmu do with Overscan. A household that adds no profile, sets no end date, sends no message and installs no theme sees nothing change.

### M6: The household IPTV hub (L)
The all-in-one stage: every player in the house gets a curated lineup through the protocol it speaks. Details in section 21.
- Account logins with a lineup, connection limits and expiry; playlist tokens.
- M3U and XMLTV output per account.
- Xtream-compatible output: live, movies, series, guide, catch-up.
- Rule-based lineup customization, several lineups, merged channels with failover (a backup stream picked by hand can come earlier; section 7).
- Output profiles (TS, HLS, transcode presets) per account or device.
- HDHomeRun emulation for Plex, Jellyfin and Emby Live TV (stretch).
- Xtream protocol notes, a conformance suite and compatibility matrix entries.
*Done when:* TiviMate (Xtream login) and Kodi (M3U and XMLTV) show the same curated lineup as the MAG, per-account limits hold, and a rule-based edit survives a source refresh.

### M7: 1.0 (M)
Security review (an outside one if possible), complete docs, a promise that upgrades from here on keep data, packaging polish, the public name and trademark check if still open, v1.0.0.

---

## 15. Testing and quality

| Layer | Approach |
|---|---|
| Unit | Standard `go test`, table-driven. |
| Adapters | Fictional recorded fixtures and mock upstream servers; no real provider data. A Jellyfin container with generated clips for the Jellyfin adapter. |
| Conformance | Black-box suite run over HTTP against any build. Seeded from the fictional fixtures and scenarios of an existing client's test suite (sign-in as a MAG, device details, login portals, blocked and unknown boxes, paged lists, token lapse and re-handshake, no-link reasons, guide per channel, past days, archive, watchdog timing, date formats, library paging, seasons and episodes, movie and episode links). An Xtream suite joins in M6. |
| Interop | The reference client from M1; StbEmu with a MAG322 profile for the MAG UI; OTT Navigator and TiviMate for the Stalker API in M5; Xtream and M3U players in M6. |
| Real device | A manual checklist in [`testing.md`](testing.md) for the MAG322w1. |
| Media | ffmpeg tests with generated clips (lavfi test sources), never copyrighted media. |
| Soak | 24-hour runs with N simulated sessions on the LXC or a self-hosted runner, not in hosted CI; record RAM and CPU; fail on regressions. CI runs a 10-minute version. |
| Upgrades | From v0.1 on, each release is tested by upgrading a database from the previous release. |
| Security | Parser fuzzing, dependency scanning, an auth and path-traversal test set. |

### Performance budgets (targets to validate)

- Idle: under about 60 MB RSS.
- Large catalog (tens of thousands of channels and titles): under about 150 MB RSS.
- Proxying a few streams: a few percent of one core.
- Setting `GOMEMLIMIT` as a guard.
- MAG UI: see section 9.

---

## 16. Deployment (Proxmox LXC)

- **Container:** Debian, unprivileged, about 1 vCPU and 512 MB to 1 GB RAM without transcoding; more with software transcoding.
- **Network:** static IP or DHCP reservation, same network as the MAG. Time synchronized (guide times depend on it).
- **Install:** the binary, ffmpeg from the distribution, a systemd unit, config in `/etc/overscan/`, data in `/var/lib/overscan/`.
- **Ports:** 8080 by default (above 1024 so an unprivileged container needs nothing extra); a reverse proxy can add 80 and TLS.
- **Hardware acceleration (optional):** pass `/dev/dri` through to the container.
- **Media:** bind-mount host folders read-only.
- **Backups:** container snapshots plus the SQLite online backup; the key file stored separately.
- **MAG setup:** in *System settings → Servers → Portals*, set a portal URL to `http://<server>:8080/c/`, as Infomir's [External portal loading](https://wiki.infomir.eu/eng/set-top-box/stb-linux-webkit/embedded-portal/external-portal-loading) page describes (menu names can differ between firmware versions). With two portals set, the box asks which one to load at start, so Overscan can sit beside an existing portal; multi-portal mode keeps up to eight, with a default that loads after a delay. On first load the box shows a pairing code to approve in the admin.
- **Docker:** a compose file for other setups. **In place:** each release archive holds a `Dockerfile` and a `compose.yaml` that build an image from its binary, on Debian's slim image with ffmpeg ([`install-docker.md`](install-docker.md)).

---

## 17. Legal, licensing, clean room

*This section is a plan, not legal advice. A short consultation before a public release is sensible.*

- **Licence (decided):** AGPL-3.0-or-later for the server and web code. The protocol notes (`docs/protocol/`), the conformance suite with its fixtures (`conformance/`) and the logo (`docs/logo/`) are Apache-2.0, so other implementers and clients can reuse them. Every file carries an SPDX identifier.
- **Contributions:** DCO sign-off on every commit, checked in CI from M1; no CLA.
- **Clean-room policy** (`docs/clean-room.md`):
  - *Allowed references:* Infomir's public documentation (the wiki, the JS API and REST references, published example portals), community documentation, open-source clients and servers under their licences (including the maintainer's own client), and redacted traffic from your own devices talking to your own server or to portals you are entitled to use.
  - *Not allowed:* leaked or third-party copies of proprietary server source, firmware extraction, or code derived from them. Contributors who have read such code disclose it and stay off the protocol modules.
  - *Old Stalker Middleware source:* copies survive in forks of a GitHub repository whose original now holds only a README. Some describe the code as open source, but it shows no licence, so treat it as proprietary under this policy.
  - *Provenance:* each protocol behavior notes where it came from.
- **Trademarks:** "Stalker", "Ministra", "MAG" and "Infomir" are used only to say what we are compatible with. They do not appear in the project name, logo or UI branding, and the UI does not imitate Infomir's design.
- **Third parties:** respect the terms of Jellyfin, Plex, TMDB and others; no bundled artwork or content.
- **Content and circumvention:** no features whose main purpose is to defeat provider limits, DRM or geo-restrictions. Documentation tells users to follow their provider's terms.
- **Hub stage:** the Xtream-compatible output serves the server's own users, and "Xtream" is named only to say what Overscan is compatible with. Overscan ships no reseller features (credits, sub-resellers, selling lines), and its docs say so plainly, because Xtream-style panels are widely associated with unlicensed resale.

---

## 18. Observability and diagnostics

- Structured, redacted logs; levels per component.
- `/healthz` and `/metrics` (active streams, upstream errors, memory).
- Per-device request log in the admin.
- Per-account activity: who is watching what, on which device, from which source.
- Capture log of unknown requests.
- A redacted support bundle that users can attach to bug reports. **In place:** offered on the admin's *Settings* page (M2, D59).
- A channel check, `overscan debug channel` (in place since 0.0.8): what a channel's source answers and what a player gets through the hub, with the stream's clock and lost packets, in a report without addresses. The admin can offer the same later.

---

## 19. Risks and mitigations

| Risk | Mitigation |
|---|---|
| The MAG322w1's old browser and weak CPU make the UI slow. | The probe page mapped the browser (ES5, CSS of Safari 7 and 8); plain DOM, performance rules, real-device checklist. |
| TypeScript no longer emits ES5 (deprecated in 6.0, removed in 7.0). | TypeScript for type checks only; Babel or SWC for ES5 output; an ES5 parse guard (section 9). |
| Stalker clients differ from the protocol notes. | Conformance suite, compatibility matrix, lenient parsing, capture log. |
| The reference client was only tested against fakes. | Treat the notes as a hypothesis; verify with the box and the reference client early. |
| Providers restrict sharing or change behavior. | Enforce connection limits, report honest errors, no evasion features. |
| Providers throttle or ban clients that crawl their catalogs. | Mirror only cheap data, fetch movies and series on demand, per-source request limits and backoff. |
| Upstream URLs or tokens leak to devices. | Proxy by default, artwork served by Overscan, redaction in logs. |
| Transcoding load is higher than expected. | Prefer redirect and remux, measured capability profiles, the media server's transcoder for library items, optional hardware acceleration. |
| Plex or Jellyfin APIs change. | Isolated adapters, version checks, adapter tests. |
| Legal or trademark objections. | Clean-room policy, nominative use of names, review before public release. |
| Scope creep. | Milestone gates, a release at the end of M2, the hub stage after households, the backlog below. |
| The hub stage overlaps mature tools (Dispatcharr, m3u-editor). | Keep the MAG and Stalker side as the core; read their output instead of competing feature for feature (principle 8). |
| The hub stage drifts toward a reseller panel. | Household scope and the non-goals hold; no credits or reseller features. |
| Firmware updates for the box stop (2.20.16 was built in August 2026). | Generic protocol, other Stalker clients on other devices. |
| Long-running memory growth. | Soak tests, memory limit, streaming parsers. |
| One maintainer. | Documentation, tests, small modules. |

---

## 20. Decisions and open questions

The reasons behind each decision are in [`decisions.md`](decisions.md).

**Decided**

- Name: Overscan.
- Language: Go (Rust stays an option behind clean seams).
- Go module path: `overscan.gureet.ca`.
- Storage: SQLite through ncruces/go-sqlite3, with migrations embedded in the binary.
- Sources live in the database, with their addresses and credentials, and the channels' stream addresses, encrypted under a key file kept outside it; a source in the configuration file is imported once (D46, which revisits D35).
- Target: a Proxmox LXC, MAG322w1 first.
- Scope: household first.
- Licence: AGPL-3.0-or-later for code; Apache-2.0 for `docs/protocol/`, `conformance/` and the logo; DCO sign-off on every commit, checked in CI from M1.
- MAG UI: one `rem`-scaled layout on a 1280×720 reference grid, measured at the box's graphics resolution: 1280×720 on the MAG322w1, which offers nothing higher. Themes change its look, not its layout (D41).
- MAG UI language: TypeScript for type checks, ES5 emitted by Babel or SWC, with an ES5 build guard, Autoprefixer for CSS and no polyfills.
- The probe page came first in M1, and its results settled the UI toolchain.
- A device API for the MAG UI; the Stalker API stays a compatibility surface for third-party clients.
- Accounts in the first schema (one household account by default); devices and profiles belong to accounts.
- Proxy by default; redirect only for sources whose URLs carry no secrets.
- Link tokens start sessions; sessions carry seeks, reconnects and segments.
- Mirror cheap data; fetch movies and series on demand with caching.
- New devices wait in a pending list for approval (with a pairing code and a device secret for the MAG UI); auto-registration off by default.
- The MAG UI keeps its device secret in a cookie, with a copy in a file saved with `gSTB.SaveUserData` (D38), and pairs by a code shown on the box; the Stalker API refuses a paired box's MAC (D39).
- Milestones: v0.1 at the end of M2, a hardening pass in every milestone, M6 is the household IPTV hub, M7 is 1.0.
- Library order: Jellyfin, then local files, then Plex.
- M3U/XMLTV and Xtream output in M6, with an admin-only debug playlist in M1.
- Ministra-style management REST is in the backlog.
- Copies of the old Stalker Middleware source are off-limits under the clean-room policy.
- Optional TMDB metadata: in M4, with the administrator's own key (D88).
- Repository: private on GitHub for now, and v0.1.0 a private release (D49).
- Logo: option C, "Bleed" (a disc cropped by a screen frame, with the cropped part ghosted out), in ink, paper and teal. Apache-2.0.
- Admin sign-in: one administrator, an argon2id password set on the server, sessions in the database, and the portal's address unless `admin_listen` names another (D44).
- Lineups and packages are one concept: the lineup says what an account sees (D45).
- Lineup edits survive refreshes, and numbers that the administrator sets are pinned (D47).
- The upstream Stalker adapter comes in M3; M2 brings Xtream (D48).
- Xtream sources are read through the API, with their login sealed, and their stream addresses are made at play time (D50).
- Each source's guide is read into the database a generation at a time, and matched to its channels by guide ID, then by name (D51).
- Films and series are read from a source when a device first asks for them, and kept in the database; details and episodes when a title is opened (D63).
- Films and episodes play through the hub, which passes on range requests (D64).
- How far the household got into a film or an episode belonged to its account until profiles came (D65); it belongs to the profile watching (D94).
- The Stalker API serves series in a `series` module of their own, a series' seasons read when a client opens it (D74), and Stalker clients play films and episodes as they are (D75).
- An upstream Stalker portal is a source of live channels and guides, read as the box that the provider registered, in one session that its streams keep alive (D76).
- A Stalker portal's films and series are read a page at a time as devices browse them, each of its categories a shelf of its own (D77).
- Shared-tuner mode: one connection to a source serves every screen on a channel, behind a setting that starts off (D93).
- Profiles keep favourites and resume points; the first takes what the account kept, and a box switches between them, with a profile's PIN if it has one (D94).
- An account's end date is set in the admin; past it, its boxes, Stalker clients and recordings play nothing (D95).
- Messages from the administrator reach the boxes that run Overscan's page, with the lineup's version; Stalker clients get none yet (D96).

**Open**

1. Answered: shared-tuner mode is in, off by default, the subscriber's call (D93).
2. A native Overscan source type in the reference client later, or is the Xtream output (M6) enough?
3. GitHub organization: move the repository before it goes public? This no longer blocks code, since the module path does not depend on it.
4. Answered: lineups and packages are one concept (D45).
5. Answered: v0.1 is a private release; the repository stays private for now (D49).
6. HDHomeRun emulation in M6 or the backlog?
7. Answered: the MAG UI keeps its device secret in a cookie, with a copy in a `SaveUserData` file (D38).

---

## 21. Later stage: the household IPTV hub (M6)

Once boxes and Stalker clients are served well, Overscan can become the one IPTV server a household runs: every player in the house gets the same curated lineup through the protocol it speaks, and each person has their own login. The stage comes after households (M5) because it builds on accounts, profiles and the stream hub. It changes none of the earlier milestones, because the data model already has accounts, lineups and lineup entries with several streams.

### 21.1 Outputs

| Output | Endpoints | Notes |
|---|---|---|
| M3U playlist | `/get.php?username=…&password=…&type=m3u_plus` for players that expect the Xtream form; `/playlist/<token>.m3u` otherwise | Per account. `tvg-*` and catch-up attributes. Every link points at the stream hub, never upstream. |
| XMLTV guide | `/xmltv.php?username=…&password=…`; `/epg/<token>.xml.gz` | Only channels the account can see; times already corrected. |
| Xtream-compatible API | `player_api.php` (account info; live, movie and series categories and lists; movie and series info; short EPG and day tables) and stream paths for live, movie, series and timeshift | Overscan's stable ids are the numeric ids players see. Live as TS or HLS per account. |
| HDHomeRun emulation (stretch) | `discover.json`, `lineup.json`, `lineup_status.json`, `device.xml` | For Plex, Jellyfin and Emby Live TV. Entering the address by hand comes first; network discovery is optional. |
| Stalker API and MAG UI | Already there | The same accounts, lineups and limits. |

### 21.2 Accounts and access

- An account has a username and a password (stored as a hash), a playlist token that can be rotated, a lineup, a connection limit, an expiry and the outputs it may use.
- MAG boxes and Stalker clients belong to accounts through their devices, so one person can watch on the living-room box and on a phone under the same limits.
- Limits are checked twice: the account's own limit and each source's limit. When a source is full, a merged channel fails over to another source; otherwise the player gets a clear error.
- M3U and Xtream clients send credentials in URLs by design, so outputs stay LAN-only by default (section 11).
- The admin's Activity page shows who is watching what, on which device, from which source.

### 21.3 Customization

- **Rules, not hand edits.** Lineup changes are ordered rules: match by source, group, name pattern, `tvg-id` or quality tag, then include, exclude, rename, regroup, renumber, set a logo, map guide data or shift guide times. Rules run after every refresh, so edits survive upstream changes; a one-off manual edit is stored as a rule too.
- **Merged channels with failover.** One lineup channel can carry several streams from different sources, in priority order.
- **Several lineups.** For example a kids' lineup or one per room; each account picks one.
- **Logos and artwork.** Upload a logo or point at one; everything is served through the artwork proxy.
- **Guide control.** Pick the guide source per channel, fix time offsets, and fill gaps with placeholder blocks so players do not show empty rows.
- **Output profiles.** Passthrough TS, HLS, or a transcode preset (for example 720p H.264 for a weak device), per account or device.
- **Movies and series.** Hide or rename categories, merge duplicates across sources, override metadata.
- **Import and export.** Lineups, rules and profiles as one versioned YAML file, for backups, sharing, or keeping a setup in git.

### 21.4 Protocol notes and conformance

- `docs/protocol/xtream-api-notes.md` (Apache-2.0), written from community documentation and tests against players, with the same documented, observed and assumed marks as the Stalker notes.
- `conformance/xtream/`: a black-box suite with fictional fixtures.
- Compatibility matrix entries per player and version, added only after testing.

### 21.5 What stays out

- Reseller panels: credits, sub-resellers, selling or renting access, branding for resale, multi-server load balancing. Overscan is for a household or a small group of friends on one server.
- Anything that hides streams from a provider or exceeds its connection limit.

### 21.6 Where it sits among other tools

Dispatcharr and m3u-editor already import M3U and Xtream and publish M3U, XMLTV, an Xtream-compatible API and HDHomeRun emulation, with per-user access (section 23). Overscan should not race them feature for feature. Its own ground is the MAG UI, the Stalker API and the library sources; the hub stage makes that ground reachable from every other player in the house. A household that prefers Dispatcharr for stream management can keep it and feed Overscan through its Xtream output (principle 8).

---

## 22. Backlog (after 1.0)

Radio and audio clubs · weather and widgets · app launcher · DVB tuner sources · HDHomeRun emulation, if it misses M6 · pseudo-channels from libraries (or via ErsatzTV or Tunarr) · multi-room resume · recordings sharing · Prometheus dashboards · plugin system · translations · a native integration with the reference client · Ministra-style management REST (moved from M5) · a web player in the admin for previewing streams · Schedules Direct as a guide source, with the user's own subscription.

---

## 23. Prior art

From their descriptions: **Stalkerhek** proxies a Stalker portal so many boxes can use one account. **Tvheadend** is a TV server for tuners and IPTV. **ErsatzTV** and **Tunarr** build live TV channels from media libraries. **Dispatcharr** (AGPL-3.0) merges M3U and Xtream sources into one lineup with guide matching, and publishes M3U, XMLTV, an Xtream-compatible API and an emulated HDHomeRun tuner, with per-user access and transcoding profiles. **m3u-editor** edits playlists and guides and offers Xtream API output and HDHomeRun emulation; **Threadfin** proxies M3U and XMLTV for Plex, Jellyfin and Emby. **ProxyStream** is a small project that merges Xtream, M3U and Stalker providers and emulates the Stalker API alongside Xtream and M3U output. **iptvnator** is an IPTV player.

None of them serves MAG boxes a native portal UI with a guide, libraries and household features, which is the gap Overscan fills. The hub stage (section 21) overlaps Dispatcharr and m3u-editor, so Overscan meets them through interoperability rather than feature races: ErsatzTV and Tunarr can feed Overscan through M3U and XMLTV, and Dispatcharr through its Xtream output.

---

## Appendix A: MAG322w1 facts

From Infomir's datasheet and product pages: BCM75839 chipset, MIPS 4KE dual core at 750 MHz, 512 MB RAM and flash, Linux 3.3, OpenGL ES 2.0, HEVC support, 100 Mbps Ethernet, Wi-Fi b/g/n (150 Mbps, w1 variant), two USB 2.0 ports, HDMI 1.4, compatible with the MAG JS API. The latest firmware news found (2.20.09, June 2019) added Opus audio support and mentions multi-portal mode, but firmware kept coming: 2.20.14 and 2.20.16 have JS API version 349, and the box reports its model as `MAG322`. Prefer Ethernet for streaming.

## Appendix B: Glossary

- **Portal / middleware:** the server a set-top box talks to for channels, guide and VOD.
- **MAG:** Infomir's set-top box line.
- **Reference client:** a Stalker client written by the maintainer, and the first client that Overscan works with. Its code is one of the inputs to the protocol notes.
- **Handshake:** the first call that gives a box a token.
- **`cmd`:** the portal's command for an item, which `create_link` turns into a playable address.
- **Catch-up / archive:** watching past programmes.
- **Remux:** changing the container without re-encoding.
- **Device API:** the JSON API our MAG UI uses, separate from the Stalker API.
- **Account:** who may watch what: a lineup, limits, expiry and, from M6, a login. Devices and profiles belong to an account; a household starts with one.
- **Lineup:** the editorial view of live TV: numbers, names, groups, logos, guide mapping. It also says what an account may see; there are no separate packages (D45).
- **Xtream:** a widely used IPTV API (`player_api.php`) that many players speak. Overscan reads it from M2 and serves it from M6.
- **HDHomeRun emulation:** answering like a network TV tuner, so Plex, Jellyfin and Emby Live TV can use Overscan.
- **Hub stage:** M6, when Overscan also publishes M3U/XMLTV, Xtream and HDHomeRun outputs for every player in the house.
- **Overscan:** the part of a picture beyond the visible edge of a TV, and the name of this project.
