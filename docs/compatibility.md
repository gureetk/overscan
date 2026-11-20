# Compatibility with other apps

Besides the MAG box's own page, Overscan serves apps of two kinds: MAG emulators, which load the portal's page as a box does, and Stalker clients, which bring their own interface and call the Stalker API. This page says how to connect three apps that the plan names, StbEmu, OTT Navigator and TiviMate, what each gets from Overscan, and what each did when it was tried. Players that read a playlist or sign in with a login, such as VLC, Kodi and TiviMate, have a section of their own.

An entry says which version of the app was tried, on what kind of device, and against which version of Overscan, in general terms. Until someone tries an app, its column says *Not tried yet*: nothing here is assumed to work because the protocol notes say it should. Reports are welcome, as issues: say what the app showed, and add what the admin's *Capture log* lists, which is what the app asked for that Overscan does not answer, with values that could identify a device or an account already taken out.

## Connecting

| | StbEmu | OTT Navigator | TiviMate |
|---|---|---|---|
| What it is | An app that emulates a MAG box | A player app with a Stalker portal option | A player app for televisions with a Stalker portal option |
| What it uses | Overscan's page, as a box does: it loads the portal's page and emulates the box's JavaScript API ([StbEmu's documentation](https://docs.stbemu.com/)) | The Stalker API | The Stalker API |
| Address to give it | `http://<server>:8080/c/` | `http://<server>:8080/stalker_portal/c/` | `http://<server>:8080/stalker_portal/c/` |
| Settings | A profile with the STB model MAG322 and the screen resolution 1280×720 | A MAC address: the app's own, or one you give it | The same |
| Approving it | The page shows a pairing code, which the admin's *Devices* page approves | The first connection waits for approval on the *Devices* page, by its MAC address | The same |

The admin's *Settings* page shows both addresses for the server. A Stalker client may not use the MAC address of a box paired with Overscan's page: the Stalker API refuses it (D39). Once approved, a Stalker client plays as the profile that the *Devices* page chooses for it (D94).

## What Overscan gives each

StbEmu runs Overscan's page, so it gets what a box gets: Home, Live TV, the guide with catch-up, films and series, radio, recordings, profiles, messages and themes. It plays streams with the player of the device it runs on, not the box's, so the page's playback check, run in StbEmu, decides what plays as it is there. It tells nothing of how fast the page is on a real box.

A Stalker client gets what [section 11 of the protocol notes](protocol/stalker-protocol-notes.md#11-what-overscan-answers-so-far) lists:

- the lineup's channels by group, with logos, and their streams through Overscan;
- the guide: what is on now and next, and each day's programmes;
- past programmes from the archive of a channel whose source keeps one;
- films and series, with seasons and episodes, which play as they are, with seeking;
- the favourite channels of its profile, which it can list but not change;
- the household's end date, which it can read once the account has ended, when everything else is refused.

It gets no messages, since what a portal sends a client for one is not known from an allowed source (D96), and none of the page's own screens. Anything else it asks for answers 404, and goes to the capture log.

## What each did

| | StbEmu | OTT Navigator | TiviMate |
|---|---|---|---|
| Version tried | Not tried yet | Not tried yet | Not tried yet |
| Signing in and approval | Not tried yet | Not tried yet | Not tried yet |
| Channels and groups | Not tried yet | Not tried yet | Not tried yet |
| Playing a channel, and changing channels | Not tried yet | Not tried yet | Not tried yet |
| The guide | Not tried yet | Not tried yet | Not tried yet |
| Past programmes | Not tried yet | Not tried yet | Not tried yet |
| Films | Not tried yet | Not tried yet | Not tried yet |
| Series | Not tried yet | Not tried yet | Not tried yet |
| Favourites | Not tried yet | Not tried yet | Not tried yet |
| An ended account | Not tried yet | Not tried yet | Not tried yet |
| What the capture log shows | Not applicable: it uses the page | Not tried yet | Not tried yet |

## Players that use the outputs

Players other than the boxes reach Overscan through an account's playlist and guide, or its Xtream login, both off until turned on, on the admin's *Accounts* page ([outputs.md](outputs.md), D103, D104). They are not devices: nothing needs approving, and the account's login or its playlist's token lets them in.

| | VLC | Kodi | TiviMate | OTT Navigator |
|---|---|---|---|---|
| What it uses | The account's M3U playlist | The account's playlist and XMLTV guide, through its PVR IPTV Simple Client add-on | The account's Xtream login, or its playlist | The account's Xtream login |
| Address to give it | `http://<server>:8080/playlist/<token>.m3u` | The playlist's, and `http://<server>:8080/epg/<token>.xml.gz` as the guide's if the add-on does not take it from the playlist | `http://<server>:8080`, the username and the password, as an *Xtream Codes* playlist; or the playlist's address | The server's address, the username and the password, as an Xtream provider |

Each gets the channels that the account's lineup shows, in its order, without the locked ones once the account has a parental PIN; a playlist holds channels only, and an Xtream login gives films and series too. Every stream plays through Overscan, counted against the account's limit and its source's.

| | VLC | Kodi | TiviMate | OTT Navigator |
|---|---|---|---|---|
| Version tried | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| Channels and groups, in the lineup's order | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| Playing a channel, and changing channels | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| The guide, at the right times | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| Past programmes | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| Films and series | Not applicable: a playlist holds channels only | Not applicable: the same | Not tried yet | Not tried yet |
| The account's limit | Not tried yet | Not tried yet | Not tried yet | Not tried yet |
| Live channels made H.264 | Not tried yet | Not tried yet | Not tried yet | Not tried yet |

## Trying an app

1. Run the newest release, with a source of channels and a guide, and a source of films and series if you have one.
2. Connect the app as above, and approve it on the *Devices* page. For a Stalker client, the log says `new device waiting for approval` with its MAC address first.
3. Go through the rows of the table above: the channels by group, a few channels played and changed, the guide, a past programme on a channel with an archive, a film and an episode with seeking, and the profile's favourites.
4. An ended account (optional): set the household's end date to yesterday on the *Settings* page, see what the app says, then remove it.
5. For a Stalker client, open the *Capture log*, and note what it lists.
6. Report what worked and what did not, with the app's version and what the log said around it.
