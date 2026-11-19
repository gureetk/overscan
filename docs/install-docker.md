# Running Overscan in Docker

The first target is a Proxmox container ([`install-lxc.md`](install-lxc.md)), but every release archive also holds a `Dockerfile` and a `compose.yaml`. They build a small image from the archive's own binary: Debian's slim image with ffmpeg, which repairs streams (D57). Nothing is pulled from a registry but Debian's image and packages.

## Start it

On a machine with Docker and its Compose plugin, on the same network as the MAG box, take the archive for the machine (`linux_amd64` for 64-bit x86, `linux_arm64` for 64-bit ARM, such as a Raspberry Pi 4 or 5) and, in the unpacked folder:

    docker compose up -d --build

Overscan then listens on port 8080 of the machine, and keeps its data in a volume named `overscan_data`: the database, the key file (`secret.key`, D46) and the logo cache. Set the administrator's password, then sign in to the admin at `http://<machine>:8080/admin/` and add your sources there:

    docker compose exec overscan overscan admin password

A MAG box uses `http://<machine>:8080/c/` as its portal, as [`trying-it.md`](trying-it.md) says.

## Settings

The image has no configuration file: Overscan uses its defaults and the `OVERSCAN_*` environment variables in `compose.yaml` (`overscan.example.yaml` lists them). For more, copy `overscan.example.yaml` to `overscan.yaml` beside `compose.yaml`, change it, and remove the `#` before its line under `volumes:` in `compose.yaml`. Leave `data_dir` and `ffmpeg` as they are. Then:

    docker compose up -d

The container runs as its own user, with a read-only file system apart from its volume, no privileges and no capabilities.

## Manage

    docker compose logs -f                                   # the log
    docker compose exec overscan overscan devices list       # devices, as in install-lxc.md
    docker compose exec overscan overscan debug channel 42   # a channel check

The admin's *Settings* page also offers a support bundle for bug reports.

## Back up

The key file never changes: keep a copy of it apart from everything else, for example as a note in a password manager.

    docker compose exec overscan cat /var/lib/overscan/secret.key

To copy the database out of the container, into the current folder:

    docker compose exec overscan overscan backup /var/lib/overscan/backups/overscan.db
    docker compose cp overscan:/var/lib/overscan/backups/overscan.db ./overscan.db
    docker compose exec overscan rm /var/lib/overscan/backups/overscan.db

[`backup.md`](backup.md) says more. To restore `overscan.db` from the current folder, the server stopped meanwhile:

    docker compose stop
    docker compose run --rm -T --entrypoint sh overscan \
      -c 'cd /var/lib/overscan && cat >overscan.db && rm -f overscan.db-wal overscan.db-shm' <overscan.db
    docker compose start

## Upgrade and remove

To upgrade, unpack the new archive anywhere and run `docker compose up -d --build` in it. The project's name in `compose.yaml` is fixed, so the new container takes over the same volume, whichever folder it is in. Copy the database first to be able to go back.

To remove the container and its image, but keep the data:

    docker compose down --rmi local

`docker compose down --volumes` removes the data too.
