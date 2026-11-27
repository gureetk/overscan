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
  <a href="https://github.com/gureetk/overscan/releases/latest"><img alt="Newest release" src="https://img.shields.io/github/v/release/gureetk/overscan?label=release&color=F5F3EC&labelColor=0F1424"></a>
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

It is made for the home network. Away from home, reach it through a VPN into
your home network rather than by opening its port to the internet: its
security has been reviewed by the project, not from outside
([docs/security.md](docs/security.md)).

Overscan comes with no channels, films or playlists, and points to none. It
plays only the sources its administrator adds: use it with services and media
you are entitled to use.

## Status

**Version 1.2.0 is the newest release.** It ends the ninth milestone, guides
and pictures, described below. Since 1.0, Overscan promises that what is there is
safe, documented, installable and kept across upgrades: the security review is
written up in [docs/security.md](docs/security.md), and an upgrade keeps what a
household set up, which CI checks with every change from every full release
since 0.3.0 ([docs/upgrading.md](docs/upgrading.md)).

It plays live channels from M3U playlists, Xtream servers and providers' Stalker
portals, with their programme guides, on a MAG322w1 through Overscan's own
interface, and in apps that speak the Stalker portal protocol. The admin adds
sources, arranges the lineup (groups, names, numbers, hidden channels, parental
locks), approves devices and shows what is playing. The stream hub proxies every
stream, relays HLS, leaves out what a source sends again, and can repair a
channel's timestamps with ffmpeg, or transcode channels with profiles of the
household's own, on the server's Intel or AMD graphics where it has them
([docs/transcoding.md](docs/transcoding.md)). Overscan installs as a service
in a Proxmox container or another Debian system, from an archive or a Debian
package, or runs in Docker, from the archive or the published image.

The box and Stalker clients browse and play the films and series of Xtream
servers, Stalker portals, Jellyfin and Plex servers and folders on the server,
with seeking and resuming. Films whose sound the box cannot play go through
ffmpeg; those whose picture it cannot play are transcoded by Jellyfin or, where
the administrator lets it, by Overscan's own ffmpeg, which can also transcode
films, shelves or titles always with a profile; subtitles beside a film or
in Jellyfin show on the box, and a playback check on each box measures what it
plays. Past programmes play from the archives of the sources that keep them.
Films and series can be locked with the parental PIN, as channels can; radio
stations play on a screen of their own; a folder's titles can get their details
from TMDB; boxes can record programmes from the guide on the server; and, where
the administrator turns it on, they can pause live TV and go back in it.

Each person in a household can have a profile on the box, with their own
favourites and resume points. The administrator can set an end date, send
messages to the boxes and give them a theme, and, where it is turned on, screens
on one channel share one connection to its source.

Since 0.5.0, players other than the boxes reach Overscan too. Accounts get a
login, a limit on their streams and, where the administrator chooses, only some
of the sources, and each can serve players an M3U playlist and XMLTV guide, or
an Xtream-compatible login for players such as TiviMate, behind switches that
start off, on the home network only. Where an account turns it on, its players
can play live channels as HLS, which Overscan cuts from each channel's one
stream, or from one transcode of it, and shares among them. There can be several
lineups, one for each account to choose, with rules that keep renaming,
regrouping, numbering, hiding and fixing the guide of channels as sources
change; a channel can have backups that play when its own stream cannot start,
and a rule can merge the copies of a channel that sources list more than once;
live channels can be made H.264 for a device that needs it; and the lineups
export to one file, and import from it.

Since 1.1.0, Overscan can be a household's playlist manager. Each source can
bring only the categories ticked; a lineup can be made of the household's own
groups, which take categories from any source and can number their channels
from a first number; groups sort by name and are renamed by find and replace;
and a merge can put sources in order, or show the copy with the best picture
seen. Channels that fail to start as people watch can be
hidden until they play again, the admin shows what each stream holds, and the
*Overview* can stop a stream. Overscan never opens a channel just to test it.

Since 1.2.0, a household can add XMLTV guides of its own for the channels
whose sources give none, matched by guide ID and then by name, and choose a
channel's guide by hand; the *Guides* page shows every guide, the sources'
first, and what each fills. A channel's logo can be set by hand or by a rule,
and Overscan uses a guide's icon where a channel's own logo does not load.
Placeholder programmes can fill a guide's gaps; films and series of Xtream
servers and Stalker portals can take TMDB's pictures where theirs are missing,
do not load or are placeholders; and a source's channels can move to another
provider's sources, keeping their numbers, groups and favourites.

Much of what the third to ninth milestones added has been tested against test
servers and in a desktop browser, but not yet on a MAG box or with real
providers, media servers and players, so it may not yet work with yours:
[docs/compatibility.md](docs/compatibility.md) lists what has been tried.
[docs/guide.md](docs/guide.md) says how to do each task, and
[docs/trying-it.md](docs/trying-it.md) how to try it.

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
clients are expected to work and are tracked in a compatibility matrix,
[docs/compatibility.md](docs/compatibility.md), as they are tried, not assumed.

## Documentation

| Document | What is in it |
| --- | --- |
| [docs/guide.md](docs/guide.md) | Running Overscan, task by task: where each is done, and which page says more |
| [docs/troubleshooting.md](docs/troubleshooting.md) | What to check when something goes wrong, by what you see |
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
| [docs/themes.md](docs/themes.md) | Themes for the boxes: what a theme file holds, and installing one |
| [docs/rules.md](docs/rules.md) | A lineup's rules: what they match, what they do, and in what order |
| [docs/lineup-file.md](docs/lineup-file.md) | Lineup files: exporting every lineup, and importing them on this server or another |
| [docs/outputs.md](docs/outputs.md) | Players other than the boxes: an account's playlist and guide, and its Xtream login |
| [docs/compatibility.md](docs/compatibility.md) | What StbEmu, OTT Navigator, TiviMate, VLC and Kodi get from Overscan, and what they did when tried |
| [docs/api/admin.yaml](docs/api/admin.yaml) | The admin's JSON API, as an OpenAPI 3.1 description |
| [web/admin/README.md](web/admin/README.md) | The admin's pages and the files they are built from |
| [conformance/README.md](conformance/README.md) | Black-box tests of what Stalker clients see |
| [web/mag/README.md](web/mag/README.md) | The MAG page: how it is built, and trying it in a desktop browser |
| [docs/clean-room.md](docs/clean-room.md) | How the protocol is documented without copying proprietary code |
| [docs/decisions.md](docs/decisions.md) | Decision log: what was chosen and why |
| [docs/probe.md](docs/probe.md) | The probe page: running it on a box, and what its report contains |
| [docs/trying-it.md](docs/trying-it.md) | A walk through Overscan in the order you set it up: the admin and its sources, a MAG box, a Stalker client, players and a channel check |
| [docs/testing.md](docs/testing.md) | How Overscan is tested: CI, conformance, the soak test, and checklists for the box and the admin |
| [docs/install-lxc.md](docs/install-lxc.md) | Installing Overscan as a service in a Proxmox container or on another Debian system, from an archive or the Debian package |
| [docs/install-docker.md](docs/install-docker.md) | Running Overscan with Docker Compose, from a release archive or, from v1.0.0, the published image |
| [docs/backup.md](docs/backup.md) | What to back up, the `overscan backup` command, and restoring |
| [docs/upgrading.md](docs/upgrading.md) | Upgrading: what an upgrade keeps, from which versions, and going back |
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
protocol. [CONTRIBUTING.md](CONTRIBUTING.md) has the details, and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) says what is expected of everyone who
takes part.

Copyright © Overscan contributors.
