<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/logo/overscan-lockup-on-dark.svg">
    <img alt="Overscan" src="docs/logo/overscan-lockup-on-light.svg" width="420">
  </picture>
</p>

<p align="center">
  <strong>A free, self-hosted portal server for Stalker-compatible set-top boxes and apps.</strong>
</p>

<p align="center">
  <a href="LICENSE"><img alt="Licence: AGPL-3.0-or-later" src="https://img.shields.io/badge/licence-AGPL--3.0--or--later-3DD9C1?labelColor=0F1424"></a>
  <img alt="Status: early development" src="https://img.shields.io/badge/status-early_development-F5F3EC?labelColor=0F1424">
</p>

---

Overscan is an open-source replacement for the proprietary portal software that
Stalker-compatible set-top boxes expect to talk to. Point a box (an Infomir MAG
or any app that speaks the Stalker portal protocol) at Overscan, and it serves
live TV, video on demand, series, EPG and catch-up from the sources you already
have.

It is built for a household or a small group of friends running one server at
home, not for a reseller business. One binary, one SQLite file and ffmpeg, in a
small LXC or VM, should be all it takes.

Overscan comes with no channels, films or playlists, and points to none. It
plays only the sources its administrator adds: use it with services and media
you are entitled to use.

## Status

**Early development.** Version 0.1, the end of the second milestone, "Live TV",
is the first release meant for a household to try. It plays live channels from
M3U playlists and Xtream servers, with their programme guides, on a MAG322w1
through Overscan's own interface, and in apps that speak the Stalker portal
protocol. The admin adds sources, arranges the lineup (groups, names, numbers,
hidden channels, parental locks), approves devices and shows what is playing.
The stream hub proxies every stream, relays HLS, leaves out what a source sends
again, and can repair a channel's timestamps with ffmpeg. Overscan installs as a
service in a Proxmox container or another Debian system, or runs in Docker.
The third milestone, "Libraries", has begun: the MAG interface browses and plays
the films and series of Xtream servers, Jellyfin and Plex servers and folders
on the server, with seeking and resuming, in pre-releases, and Stalker clients
browse and play them too. A provider's Stalker portal is a source of live
channels, guides, films and series, read as the box that the provider
registered, its films and series a page at a time as boxes browse them. Films
whose sound the box cannot play go through ffmpeg; those whose picture it cannot play are
transcoded by Jellyfin or, where the administrator lets it, by Overscan's own
ffmpeg; subtitles beside a film or in Jellyfin show on the box, and a playback
check on each box measures what it plays. The fourth milestone is built, in
pre-releases that wait for tests on a box: past programmes play from the
archives of the Xtream servers, playlists and Stalker portals that keep them,
on the box and in Stalker clients; films and series can be locked with the
parental PIN, as channels can; radio stations play on a screen of their own;
a folder's titles can get their details from TMDB; boxes can record
programmes from the guide on the server; and, where the administrator turns it
on, they can pause live TV and go back in it.
[docs/trying-it.md](docs/trying-it.md) says how to try it.

## What it is meant to do

- **Serve the Stalker portal protocol** to MAG boxes and to third-party apps
  that implement a Stalker client, with a web UI for the box that Overscan
  ships itself.
- **Ingest many kinds of source:** M3U and XMLTV, Xtream-style providers,
  upstream Stalker portals, Jellyfin, Plex and local media files. Tuner back
  ends (Tvheadend, HDHomeRun, Enigma2) come later.
- **Convert between protocols:** present an Xtream source as a Stalker portal,
  and a Stalker or Jellyfin library as an M3U/XMLTV or Xtream feed.
- **Play anything the box can play:** redirect, proxy, remux or transcode per
  device, using ffmpeg and a capability profile for each device model.
- **Be easy to run:** an htmx-driven admin UI, a documented REST API with an
  OpenAPI description, and sensible defaults for a Proxmox LXC.

See the non-goals in the plan: no billing, no reseller tooling, no content
shipped with the project.

## Compatibility

The primary target is the **Infomir MAG322w1**. Other MAG models and Stalker
clients are expected to work and will be tracked in a compatibility matrix as
they are tested against a conformance suite, not assumed.

## Documentation

| Document | What is in it |
| --- | --- |
| [docs/plan.md](docs/plan.md) | The full project plan: goals, architecture, milestones, risks |
| [docs/protocol/stalker-protocol-notes.md](docs/protocol/stalker-protocol-notes.md) | What the Stalker client protocol looks like on the wire |
| [docs/protocol/xtream-api-notes.md](docs/protocol/xtream-api-notes.md) | What a client sees when it reads a provider through the Xtream API |
| [docs/protocol/jellyfin-api-notes.md](docs/protocol/jellyfin-api-notes.md) | What Overscan asks of a Jellyfin server for its films, series and files |
| [docs/protocol/plex-api-notes.md](docs/protocol/plex-api-notes.md) | What Overscan asks of a Plex Media Server for its films, series and files |
| [docs/protocol/catchup-notes.md](docs/protocol/catchup-notes.md) | How playlists and Xtream servers describe their archives of past programmes |
| [docs/protocol/playlist-notes.md](docs/protocol/playlist-notes.md) | What Overscan reads from a playlist's channels, such as which are radio stations |
| [docs/protocol/tmdb-notes.md](docs/protocol/tmdb-notes.md) | What Overscan asks of TMDB for the titles in folders, and what TMDB asks in return |
| [docs/library-folders.md](docs/library-folders.md) | How Overscan reads a folder of films and series: names, .nfo files and pictures |
| [docs/api/device.md](docs/api/device.md) | The device API that Overscan's MAG page uses |
| [docs/api/admin.yaml](docs/api/admin.yaml) | The admin's JSON API, as an OpenAPI 3.1 description |
| [web/admin/README.md](web/admin/README.md) | The admin's pages and the files they are built from |
| [conformance/README.md](conformance/README.md) | Black-box tests of what Stalker clients see |
| [web/mag/README.md](web/mag/README.md) | The MAG page: how it is built, and trying it in a desktop browser |
| [docs/clean-room.md](docs/clean-room.md) | How the protocol is documented without copying proprietary code |
| [docs/decisions.md](docs/decisions.md) | Decision log: what was chosen and why |
| [docs/probe.md](docs/probe.md) | The probe page: running it on a box, and what its report contains |
| [docs/trying-it.md](docs/trying-it.md) | How to try what works so far: the admin and its sources, a MAG box, a Stalker client, a debug playlist and a channel check |
| [docs/testing.md](docs/testing.md) | How Overscan is tested: CI, conformance, the soak test, and checklists for the box and the admin |
| [docs/install-lxc.md](docs/install-lxc.md) | Installing Overscan as a service in a Proxmox container |
| [docs/install-docker.md](docs/install-docker.md) | Running Overscan with Docker Compose, from a release archive |
| [docs/backup.md](docs/backup.md) | What to back up, the `overscan backup` command, and restoring |
| [docs/security.md](docs/security.md) | The security model, what protects what, and the known limits |
| [docs/logo/README.md](docs/logo/README.md) | Logo files, palette and usage |

## Licence

Overscan is free software. Unless a file or directory says otherwise, it is
licensed under the **GNU Affero General Public License, version 3 or (at your
option) any later version** (`AGPL-3.0-or-later`). See [LICENSE](LICENSE).

| Path | Licence |
| --- | --- |
| Everything not listed below | `AGPL-3.0-or-later` |
| `docs/protocol/` and `conformance/` | `Apache-2.0`, so other implementations and test suites can reuse them freely |
| `docs/logo/` | `Apache-2.0` (the wordmark is outlined from Poppins Bold, SIL OFL 1.1) |
| `web/mag/src/assets/fonts/` | `OFL-1.1`: Instrument Sans, by the Instrument Sans Project Authors, with its licence beside it |

The full text of each licence is in [LICENSES/](LICENSES/).

## Trademarks and affiliation

Overscan is an independent project. It is not affiliated with, endorsed by, or
sponsored by Infomir. "Stalker", "Ministra" and "MAG" are used only to say
which protocol and devices Overscan works with. They are the property of their
respective owners, and none of their names or marks are used in Overscan's name,
logo or interface.

## Contributing

Code contributions are not open yet. When they are, contributions will use the
[Developer Certificate of Origin](https://developercertificate.org/) (a
`Signed-off-by` line on each commit) and no CLA, and the clean-room rules in
[docs/clean-room.md](docs/clean-room.md) will apply to anything touching the
protocol. [CONTRIBUTING.md](CONTRIBUTING.md) has the details.

Copyright © Overscan contributors.
