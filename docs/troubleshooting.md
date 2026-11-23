# Troubleshooting

What to check when something goes wrong, by what you see. [guide.md](guide.md) says where each setting is.

**Start with the log.** On a service install, `journalctl -u overscan -f` shows it as it is written; with Docker, `docker compose logs -f`. Each line says what happened in plain words, with the source, channel or device it is about. Addresses of sources and streams never appear in it, only their hosts.

## The server

**It does not start after an upgrade.** Its log says why in its last lines.

- *the backup failed, so the database is as it was*: before it changes the schema, the server copies the database into `backups` beside it, and could not (D117). The disk is usually full: free some room, and start it again.
- *the database's schema version is N, newer than the M this build knows*: a newer version upgraded the database, and an older one cannot open it. Install the newer version again, or go back as [upgrading.md](upgrading.md) says, with the copy made before the upgrade.
- *config /etc/overscan/overscan.yaml, line N: …*: the configuration file has a mistake on that line, such as a setting that does not exist (the message names the nearest one), a value of the wrong kind, or indentation with tabs. *config: listen …* means an address without its port. The message never shows the file's values. [`deploy/overscan.example.yaml`](../deploy/overscan.example.yaml) explains every setting.

**The admin's page does not open.** Check that the server runs (`systemctl status overscan`), and the address: `http://<server>:8080/admin/`, unless `admin_listen` gives the admin an address of its own, which the log's `admin listening` line names.

**The admin says to wait before signing in again.** Five wrong passwords from one address, or fifty from all addresses, within 15 minutes make sign-ins wait (D44). Wait as long as it says. A forgotten password is set again on the server with `overscan admin password`.

## Sources

**A source's card says that its last read failed.** Its card, and the log's `source refresh failed` line, say why: the server answered with an error, an address cannot be reached, or the login was refused. What it listed before stays.

- *the source may reach public addresses only, and … is not one*: the source is kept to public addresses (D121), and its address, or one that it leads to, is on your own networks. Untick *Public addresses only* on its *Change* page if the source is meant to be at home.
- *the playlist is not a regular file*, *the playlist file does not exist*, *Overscan may not read the playlist file*: a playlist file's path names something else, nothing, or a file that the `overscan` user cannot read.
- *its address was saved with another key file*: the key file is not the one that saved it. Put the right key file back, or enter the address again ([backup.md](backup.md)).

**Channels, films or series are missing from a source.** Its card's *Categories it brings* says whether it brings every category or only those ticked; *Choose categories* shows each category with how many channels or titles it held at the last read. On *Live TV*, a channel marked *Left out* is in a category that the source no longer brings, and one marked *Missing* is one that the source no longer lists: each stays a week, then goes. The log's `source refreshed` line counts what was left out (`left_out=`).

**The guide is empty, or its times are wrong.** A source's card says when its guide was last read, with how many channels and programmes, and the log says `guide read`, or `guide read failed`. A channel without a guide of its own can take another's with a rule, and a rule can move a guide's times for a channel that shows its programmes later than the guide says ([rules.md](rules.md)). Past programmes that start at the wrong time need the source's *Catch-up time zone*.

## Boxes and Stalker apps

**A box shows *Waiting for approval* with a code.** Approve it on *Devices*, or with `overscan devices approve <code>`. A new box that does not appear there may be behind a full list: at most 100 devices wait at a time, so approve it by its MAC address, or remove those that should not wait.

**A box shows *This box is blocked*.** It was blocked on *Devices*: approve it again there.

**A box shows *This account has ended*.** Its account's end date has passed: change it or remove it on *Accounts*.

**A Stalker app cannot connect.** Its first connection waits for approval on *Devices*. It needs a MAC address of its own: the Stalker API refuses the MAC address of a box paired with Overscan's page.

**A channel does not play.** The box says why. A channel that is missing altogether may be hidden, in a group that is hidden, in another lineup, or from a source that the account does not use (*Sources* on its page).

- *All connections to this source are in use.*: the source plays as many streams as its *Streams at once* allows, and Overscan never goes over it. The log says `stream refused: the source is at its connection limit`. *Overview* → *Playing now* says what holds the connections, and *Stop* ends a stream that a forgotten player holds. Stop a stream elsewhere, or turn on the *Shared tuner* if screens watch the same channel.
- *This account plays as many streams at once as it may. Stop one elsewhere first.*: the account's limit, on its page on *Accounts*. The log says `stream refused: the account plays as many streams as it may`.
- Nothing plays, or the picture stays black: the log says `a stream's source refused it` with what the source answered, or `cannot reach a stream`. A channel can have backups that play when its own stream cannot start. `overscan debug channel <number>` checks the channel from the server ([trying-it.md](trying-it.md), section 6).

**A channel has disappeared from the boxes, and *Live TV* says *Hidden until it plays*.** Its stream failed to start, and it was hidden until it plays again, on *Overview* → *Channels that failed* or its panel. Show it again there, or give it a backup: it shows again by itself once its stream plays, as another channel's backup or a merged copy.

**A channel stutters, skips or jumps back.** Set it to be repaired on *Live TV* (D57): its stream then goes through ffmpeg, which the log's start lines say it found.

**A film does not play, or plays without sound or picture.** The box's *Settings* → *Playback Check* learns what the box plays, and the admin's *Settings* → *Converting and transcoding* says what may go through ffmpeg. The log says whether ffmpeg and ffprobe were found as the server started. *The server is transcoding as many films as it may at once.* means that the number set there is reached.

## Players

**A player gets *404 page not found* for its playlist or login.** The outputs are off: *Accounts* → *Outputs for players*, and the account's own *Playlist and guide* or its login. New addresses from *Make new addresses* replace the old ones, which then answer 404.

**A player gets *403*: *Overscan's outputs answer players on its own networks only.*** The player is not on your own networks, or comes through a reverse proxy that names no client. Away from home, connect through a VPN into your home network; behind a reverse proxy, name it in `trusted_proxies` ([outputs.md](outputs.md), D119).

**A player's login fails though it is right.** After five wrong logins from one address within 15 minutes, even the right one waits until the first of them is 15 minutes old.

## Recordings

**A recording failed.** *Settings* → *Recordings*, and the box's *Recordings* screen, say why for each: the source was at its limit, the folder was full, the account no longer uses the channel's source, or the source refused the stream. On a source that allows one stream, the box and a recording of another channel take turns; with the *Shared tuner*, a recording of the channel that the box watches shares its connection.

## The probe page

**It says that the report was not saved.** Reports are saved only while `probe_reports: true` is in the configuration file, and only from your own networks (D120, [probe.md](probe.md)).

## Reporting a problem

The support bundle, on *Settings*, holds the settings, counts of the lineup and the log's last lines, without addresses, logins or most of each MAC address. Say what you did, what you expected and what happened, with the bundle, in an issue on GitHub. A security problem is reported privately, as [SECURITY.md](../SECURITY.md) says.
