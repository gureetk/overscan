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

## Move to a new provider

Add the new source and let it be read. Then *Sources* → the old source's ⋯ → *Move its channels to other sources*: tick the new source, or several in the order that should win where two have a channel, and *Find matches*. Overscan matches each channel by its guide ID, then by its name, as it matches guides, and lists the matches, ticked, with the channels it found none for. Untick any that is wrong and *Move the ticked channels*: in every lineup they play from the new source, with their numbers, names, groups, logos, locks, favourites, recordings and backups, and the new source's own copies of them go. The old source stays, with the channels that did not move, until you delete it (D144).

## See where programmes come from

*Guides* lists first the guide of each of your sources: its Xtream server's or Stalker portal's, or the one that its playlist names. Each card says how many of the source's channels it gives programmes, when it was last read and when it is read next, and how its icons are used as logos; *Read now* reads it at once. A channel's own source's guide always comes first (D145).

## Add a guide of your own

*Guides* → *Add a guide*: the address of an XMLTV guide, gzip or not, or the path of a guide's file on the server. Overscan reads it at once and then every 12 hours. It fills only the channels whose own sources give them no programme: by the guide ID that a channel's source gives it, then by its name. Where the guide has the name in two countries, a country tag in the channel's name or its group, such as *UK:* or *[UK]*, chooses; where nothing does, the channel stays without a guide rather than take the wrong one. The guide's card says how many channels it fills, and the top of the page how many still have no guide (D139).

- **Several guides:** where two could fill a channel, the first does; *Move up* and *Move down* under a guide's ⋯ put them in order.
- **Public addresses only,** as for a source, for a guide on the internet.
- **Turn off** takes a guide's programmes from its channels at once; *Turn on* gives them back.

## Choose a channel's guide by hand

*Guides* → *Channels and their guides* lists the channels that have no guide, those that a guide of yours matched by name alone, and those whose guide you chose, each with what is on now. For a channel without a guide it suggests up to two close names, from your guides and from the channels whose own sources' guides fill them; *Use* takes one. *Choose…*, or *Choose its guide* in a channel's panel on *Live TV*, opens the channel's own page: where its programmes come from now, more suggestions, and a search of every guide's channels by name or guide ID. You can choose a guide's channel, another channel's guide, or no guide at all; *Find it as before* takes the choice back.

A choice belongs to the channel at its source, so it holds in every lineup and stays through the source's reads. It wins over the source's own guide, the guides' matches and a rule's *use another channel's guide*; a rule that moves the guide's times still moves them. A guide's channel that filled nothing before has no programmes until Overscan reads its guide again, which it does at once. Suggestions are never used on their own: check what is on before you take one (D140).

## Logos

A channel's logo is, in this order: one you set by hand, in its panel on *Live TV* → *Logo*, an address or *None*; a rule's; else its source's, if it loads; else its guide's, if it loads; else none, and devices show its name. Its guide's is the icon that a guide of yours gives the channel that fills it, or that its source's guide gives it. *Guides* → a guide's *Change* → *Its logos* says when a guide's icons are used: where a channel's own is missing or does not load, before it, or never. Each source's card, and its guide's card on *Guides*, has the same for its guide, *Its guide's logos*, not used until you choose. Overscan checks in the background, a logo at a time, whether the logos that channels choose between load, as soon as a guide has been read and every five minutes, so a box is never given one that does not; the panel says where each channel's logo comes from (D141).

## Placeholder programmes

*Guides* → *Placeholder programmes*, off at first, fills the hours in which a channel has no programme: boxes and players see a programme named after the channel in each half hour, hour, two hours or three hours, from midnight, that has none, over the next two days. Placeholders cannot be recorded or played from an archive, and searches do not find them (D142).

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
- **Pictures from TMDB** for the films and series of Xtream servers and Stalker portals, once that key is saved: *Settings* → *Pictures for providers' films and series*, off at first. A title whose poster is missing, does not load on a box, or is one that many of its source's titles share takes TMDB's, looked up in the background; its own comes back once it loads (D143).

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
