# Running Overscan: a guide by task

What a household does with Overscan, task by task: where in the admin each is done, and which page says more. [trying-it.md](trying-it.md) walks through all of it in order, and [troubleshooting.md](troubleshooting.md) starts from what goes wrong.

The admin is at `http://<server>:8080/admin/`. Its pages are *Overview*, *Sources*, *Live TV*, *Movies*, *Series*, *Devices*, *Accounts*, *Capture log* and *Settings*. Commands run as the user that runs the server: on a service install, `sudo runuser -u overscan -- overscan …`, which reads `/etc/overscan/overscan.yaml`; with Docker, `docker compose exec overscan overscan …`. `overscan help` lists the commands.

## Install it

- **In a Proxmox container or on another Debian system:** unpack the release's archive and run `sudo ./install.sh`, which installs the binary, the configuration file and a systemd service ([install-lxc.md](install-lxc.md)). Or install the release's Debian package, `sudo apt install ./overscan_<version>_amd64.deb`, which does the same.
- **With Docker:** unpack the archive and run `docker compose up -d --build` in it ([install-docker.md](install-docker.md)).
- **Set the administrator's password:** `overscan admin password`, which asks for it twice. The same command resets a forgotten one.
- **The configuration file**, `/etc/overscan/overscan.yaml`, holds the server's own settings: addresses, folders, logging, ffmpeg, reverse proxies. `deploy/overscan.example.yaml` in the archive explains each. Everything else is set in the admin.

## Add a source

*Sources* → *Add a source*. *Test* reads it without saving anything; once saved, Overscan reads it at once and then on its schedule, and the card says when, with how many channels and the guide's programmes.

- **A playlist:** its address, or the path of a playlist file on the server ([trying-it.md](trying-it.md), section 2).
- **An Xtream server:** its address, the username and the password.
- **A provider's Stalker portal:** the portal's address and the MAC address of the box that the provider registered, with its other details if the provider checks them.
- **Jellyfin or Plex:** the server's address with an API key or a token; their films and series appear on the boxes.
- **A folder of films and series** on the server ([library-folders.md](library-folders.md)).
- **Streams at once:** how many your account with the provider allows. Overscan never goes over it.
- **Public addresses only:** for a provider on the internet, so that nothing it lists can make Overscan reach your home network (D121).
- **What it brings:** every category, or only those you tick, for its live channels, films and series apart: the source's card → *Choose categories*. What it leaves out is not stored and has no guide. A new source can start with *Only those I tick, after its first read*, so that nothing reaches the lineup before you choose (D135).

## Arrange the channels

*Live TV*: groups, names, numbers, hidden channels, locks and stream repair, which stay as you set them when sources change (D47).

- **A group's numbers:** its *Numbers from…* button numbers its channels from a first number, such as 100, and a channel that joins the group later takes the next free number after its last; numbers you set yourself stay. *Sort by name*, under the group's ⋯, puts its channels in the order of their names, 2 before 10 (D137).
- **Find and replace** in the names of every group, with the pencil beside *Groups*, or of every shelf, beside *Shelves* on *Movies* and *Series*: to take a provider's prefix out of all of them, for example (D137).
- **Rules** change many channels at once and keep doing so: hide a category, rename, renumber, give a logo, use another channel's guide or move its times, merge the copies of a channel ([rules.md](rules.md)).
- **Backups** play in a channel's place when its own stream cannot start: a channel's panel, *Backups* (D109).
- **Channels that fail:** a channel whose stream failed to start the last time someone played it is marked *Failed to start*, and listed on *Overview* → *Channels that failed*, where it can be hidden until it plays again, one at a time or all at once; a channel's panel, *Its stream*, does the same. A hidden channel shows again by itself once its stream plays, as another channel's backup or a merged copy. Overscan learns this only as people watch, and never opens a channel to test it (D138).
- **What a stream holds:** a channel shows its picture's resolution once it has played, such as 1080i or 2160p HDR10, and its panel the codecs and frame rate; a merge rule can show first *The best picture seen* (D138).
- **Several lineups,** such as one for children: *Lineups* → copy; each account chooses the one its devices see (D106).
- **A lineup of your own groups** starts empty and holds only what its groups take: *Lineups* → *New lineup of your own groups*, optionally merging the copies of a channel from the start; add a group, then tick the categories it takes, from any source, with its *Categories…* button. A channel that a source adds to one of them joins at the next refresh, numbered after the last. The top of each lineup's page says which accounts see it, and through what (D136).
- **Export and import** every lineup as one file ([lineup-file.md](lineup-file.md)).

## Add a box

On the MAG box, set the portal to `http://<server>:8080/c/` and restart the portal. It shows a six-digit code: approve it on *Devices* (or `overscan devices approve <code>`). The box then shows Home, and comes back after a restart without another approval ([trying-it.md](trying-it.md), section 3).

- **Profiles** for each person who watches, with their own favourites and resume points, and a PIN if you like: *Devices* → *Profiles*.
- **The parental PIN** and locks: *Settings*, and the lock on a group, a channel, a shelf or a title.
- **Messages** to one box or every box: *Devices* → *Messages*.
- **A theme** for the boxes: *Settings* → *Themes* ([themes.md](themes.md)).

## Add a Stalker app

In the app, add a Stalker portal at `http://<server>:8080/stalker_portal/c/` with a MAC address of its own, never a box's. Its first connection is refused and waits for approval on *Devices* ([trying-it.md](trying-it.md), section 4; [compatibility.md](compatibility.md) for StbEmu, OTT Navigator and TiviMate).

## Serve players

VLC, Kodi, TiviMate and other players read an account's M3U playlist and XMLTV guide, or sign in with an account's Xtream login. Everything starts off: turn on *Accounts* → *Outputs for players*, then, on the account's page, its *Playlist and guide* or its *Login for players* ([outputs.md](outputs.md)). Players are answered on your own networks only, unless you choose otherwise.

## Give people accounts

*Accounts* → *Add an account*, which opens the new account's page, then move their devices to it on *Devices*. Each account has a page of its own, opened from the list, for a limit on the streams it plays at once, the lineup it sees, the sources it uses, an end date, a parental PIN, a theme, how its live channels are sent (*Live streams*), and its own playlist and login for players.

## Record and pause

- **Recordings:** *Settings* → *Recordings*, give them room. A box then records from the guide, from *Live TV* and full screen (green or REC), or by time from its *Recordings* screen. They play on *Movies* → *Recordings*.
- **Pause live TV:** *Settings* → *Pause live TV*, how much to keep.
- **The shared tuner:** *Settings* → *Shared tuner* lets screens and recordings on the same channel share one connection to its source, which matters for a source that allows one stream. Whether your provider allows it is between you and them.

## Films and series

The films and series of Xtream servers, Stalker portals, Jellyfin, Plex and folders appear on the boxes' *Movies* and *Series*, and on the admin's pages of the same names, where shelves and titles can be hidden or locked.

- **What a box cannot play** goes through ffmpeg: *Settings* → *Converting and transcoding* says what may be converted or transcoded, and on which server.
- **The playback check,** in the box's own *Settings* → *Playback Check*, measures what that box plays.
- **Details from TMDB** for a folder's titles, with a key of your own: *Settings*.

## Back up, upgrade, go back, remove

- **A backup:** `overscan backup` copies the database while the server runs; keep a copy of the key file apart, once ([backup.md](backup.md)).
- **An upgrade** keeps everything, and copies the database first when its schema changes ([upgrading.md](upgrading.md)).
- **Going back** to the version before: [upgrading.md](upgrading.md), *Going back*.
- **Removing it:** `sudo ./install.sh --uninstall`, or `sudo apt remove overscan` for the package. Both keep the configuration and the data ([install-lxc.md](install-lxc.md)).

## Away from home, and behind a reverse proxy

Overscan is made for the home network (D128). Away from home, connect to the home network through a VPN, such as WireGuard, Tailscale or your router's own, rather than forwarding Overscan's port: boxes, players and the admin then work as they do at home, and the outputs' check of your own networks lets them in.

Overscan speaks plain HTTP. If you put a reverse proxy with TLS in front of it on the home network, name the proxy's address in `trusted_proxies` in the configuration file, so that Overscan knows who each client is (D119, [security.md](security.md)).

## When something goes wrong

- **What is playing:** *Overview* → *Playing now*, each stream with what it holds, and *Stop* to end one that a forgotten player holds; a recording is called off on *Settings* → *Recordings* instead (D138).
- **The log:** `journalctl -u overscan -f` on a service install, `docker compose logs -f` with Docker. Each line says what happened in plain words.
- **The support bundle,** on *Settings*: the settings, the lineup's counts and the log's last lines, without addresses, logins or most of each MAC address, to read or to attach to a report.
- **The capture log:** what Stalker clients and players asked for that Overscan does not answer.
- **A channel:** `overscan debug channel <number>` checks what its source sends and what a player gets ([trying-it.md](trying-it.md), section 6).
- **By symptom:** [troubleshooting.md](troubleshooting.md).
