# Upgrading

How to move to a newer version of Overscan, what the upgrade keeps, and how to go back. From 1.0 on, an upgrade never loses or changes what a household set up ([D115](decisions.md)).

## What an upgrade keeps

| What | Where, in an installation with `install.sh` | |
|---|---|---|
| The database: sources and their logins, lineups with their edits and rules, groups, accounts and their logins, devices and their pairings, profiles, favourites, resume points, the parental PIN, recordings to come and done, themes, messages and every setting of the admin and of each box | `/var/lib/overscan/overscan.db` | Kept, and brought up to date as the new version starts |
| The key file, which encrypts the sources' addresses and logins | `/var/lib/overscan/secret.key` | Kept as it is |
| The configuration file | `/etc/overscan/overscan.yaml` | Kept as it is: `install.sh` writes one only where there is none |
| Recordings | `/var/lib/overscan/Recordings/`, or `recordings_dir` | Kept as they are |
| Copies of logos and pictures, and the playback check's clips | `/var/lib/overscan/artwork/` and `check/` | Kept; Overscan makes them again if they are lost |
| Timeshift buffers | `/var/lib/overscan/timeshift/` | Emptied as the server starts, as at every start |

What the household sees stays as it was: a new version turns nothing on by itself, and what it adds starts off, or as things were before.

What scripts, players and boxes use stays as it is too: the configuration file's keys and the environment variables, the commands and their options, the admin's API under `/api/admin/v1/`, the device API under `/api/device/v1/`, the Stalker API as clients use it, and the addresses of the playlists, guides and Xtream login that the outputs serve. Where one has to change, the new version still takes the old form, and this page says so under *Changes*.

## Which versions upgrade directly

Every full release from v0.3.0 on upgrades directly to the newest, and so do the pre-releases of 1.0: there is no need to install the versions between. CI checks this for each full release with every change ([D118](decisions.md)): the old release sets up a household through its own APIs, the new build starts on the same data directory, and everything the household set up must still be there and the same. Databases of older versions, such as v0.1.0, are brought up to date by the same steps, but no test covers them: back up first.

The database's schema has a version, which the log gives as the server starts (`database ready … schema=42`):

| Release | Schema |
|---|---|
| v0.3.0 | 30 |
| v0.4.0 | 35 |
| v0.5.0 | 42 |

A version refuses a database whose schema is newer than it knows, and says so in its log, so an older version cannot open a database once a newer one has upgraded it. That is what the backup below is for.

## Before upgrading

Nothing is needed: before the new version changes the database's schema, it copies the database into the `backups` folder beside it ([D117](decisions.md)):

    /var/lib/overscan/backups/before-upgrade-20270115-093000-schema42.db

The name says when the copy was made, in UTC, and which schema it has. Overscan keeps the newest three such copies and removes older ones; it never touches the copies that `overscan backup` makes. If the copy cannot be made, for example because the disk is full, the server does not start, and leaves the database as it was: free some room and start it again. An upgrade that leaves the schema as it was makes no copy.

The copy holds the database only. Keep a copy of the key file apart from it, once, as [backup.md](backup.md) says, and copy the recordings folder yourself if you want to keep the recordings safe.

## Upgrading

- **A Proxmox container or another Debian system:** unpack the new release's archive and run its `install.sh` as root ([install-lxc.md](install-lxc.md)). It replaces the binary and the systemd unit, keeps the configuration and the data, and restarts the server.
- **Docker:** unpack the new archive anywhere and run `docker compose up -d --build` in it ([install-docker.md](install-docker.md)). The new container takes over the same volume.

Then check the log for the version and the database:

    journalctl -u overscan -n 20

It says `starting version=…`, `backed up the database before upgrading its schema` when the schema changes, `applied database migration` for each step, and `database ready`.

## Going back

To go back to the version you had, in a Proxmox container or another Debian system:

1. Stop the server: `systemctl stop overscan`.
2. Put the older version's binary in place, from its unpacked archive: `install -m 0755 overscan /usr/local/bin/overscan`.
3. Restore the copy made before the upgrade, the one whose name has the older version's schema, as [backup.md](backup.md) says under *Restoring*: put it in place of `overscan.db`, and remove `overscan.db-wal` and `overscan.db-shm`.
4. Start the server: `systemctl start overscan`.

With Docker, restore the copy into the volume as [install-docker.md](install-docker.md) says, then run `docker compose up -d --build` in the older archive's folder.

What changed after the upgrade, such as a channel renamed or a recording made, is not in the copy. Recordings made after the upgrade stay in the recordings folder, but the restored database does not list them.

## Changes

What changed in a way that could matter to a script or a player, and what the new version does with the old form:

- None yet.
