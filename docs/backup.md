# Backing up and restoring

Overscan keeps everything it needs in a few files:

| File | What it holds |
|---|---|
| `/var/lib/overscan/overscan.db` | The database: sources, the lineup and its edits, devices, favourites, each box's settings and the guide. |
| `/var/lib/overscan/secret.key` | The key that encrypts the sources' addresses and logins in the database (D46). It never changes. |
| `/etc/overscan/overscan.yaml` | The configuration. |
| `/var/lib/overscan/artwork/` | Copies of channel logos, which Overscan fetches again if they are lost. |
| `/var/lib/overscan/check/` | The clips of the playback check (D70), which ffmpeg makes again if they are lost. |
| `/var/lib/overscan/Recordings/` | The recordings that boxes made (D89), if recording is on. They are not in the database's backups: copy the folder apart to keep them. |

The paths are those of an installation with `install.sh`; `data_dir`, `key_file` and `recordings_dir` in the configuration can move them. The commands below are run as root, as in [`install-lxc.md`](install-lxc.md): put `sudo` in front of each where it is installed.

## The key file

Copy the key file once and keep the copy apart from the database's backups, for example as a note in a password manager. It is one line of text:

    cat /var/lib/overscan/secret.key

A backup of the database is only as private as its key: whoever has both can read the sources' logins. Without the key, a restored database keeps everything else, and the admin's *Sources* page asks for each source's address and login again.

## A copy of the database

    runuser -u overscan -- overscan backup

writes a copy to `/var/lib/overscan/backups/`, named with the date and time in UTC, readable only by the `overscan` user. The copy is consistent even while the server runs. To write it somewhere else, name a file that does not exist yet and that the `overscan` user can write:

    runuser -u overscan -- overscan backup /var/lib/overscan/backups/before-upgrade.db

Copy the backups off the machine now and then; the backups folder is not cleaned up by itself.

## Proxmox

A backup of the whole container (*Backup* in the container's menu, or a scheduled job under *Datacenter → Backup*) holds the database, the key and the configuration together, in snapshot mode too: the database survives a snapshot as it survives a power cut. Such a backup holds the key with the database, so keep it as private as the provider's login.

## Restoring

1. Stop the server:

       systemctl stop overscan

2. Put the copy in place of the database, and remove the files that the old one left beside it:

       install -m 0600 -o overscan -g overscan BACKUP.db /var/lib/overscan/overscan.db
       rm -f /var/lib/overscan/overscan.db-wal /var/lib/overscan/overscan.db-shm

3. On a new machine, put the key file back first, readable only by the `overscan` user:

       install -m 0600 -o overscan -g overscan secret.key /var/lib/overscan/secret.key

4. Start the server, and check its log:

       systemctl start overscan
       journalctl -u overscan -n 20

A copy made by an older version is brought up to date when the server starts. A copy made by a newer version is refused: install that version, or a newer one.
