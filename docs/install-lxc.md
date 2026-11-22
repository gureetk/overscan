# Installing in a Proxmox container

Overscan's first target is an unprivileged Debian container on Proxmox VE. The release archives hold an install script and a systemd unit that work in any Debian or Ubuntu system with systemd. [`install-docker.md`](install-docker.md) is for Docker instead.

## 1. Create the container

In the Proxmox web interface, create a container from a current Debian template:

- **Unprivileged**, with **Nesting** on (both are the defaults). The service's sandboxing needs nesting.
- 1 core, 512 MB to 1 GB of memory, 4 GB of disk.
- A network address on the same network as the MAG box, fixed by a DHCP reservation or set statically, since the box is set up with it.

From the Proxmox host's shell, the same looks like this (`pveam available --section system` lists the templates; the names change with each Debian release):

    pct create 120 local:vztmpl/<debian template> \
      --hostname overscan --unprivileged 1 --features nesting=1 \
      --cores 1 --memory 1024 --swap 512 --rootfs local-lvm:4 \
      --net0 name=eth0,bridge=vmbr0,ip=dhcp --onboot 1
    pct start 120

## 2. Install Overscan

Take the archive for the container's machine from the repository's Releases page (`linux_amd64` for 64-bit x86), copy it into the container (from the host: `pct push 120 <archive> /root/<archive>`), and in the container:

    tar xzf overscan_<version>_linux_amd64.tar.gz
    cd overscan_<version>_linux_amd64
    ./install.sh

The script adds an `overscan` system user, puts the binary in `/usr/local/bin`, installs the distribution's ffmpeg if there is none (it repairs the streams of channels set to be repaired, D57; `./install.sh --no-ffmpeg` leaves it out), writes `/etc/overscan/overscan.yaml` from the example if there is none, and installs, enables and starts the `overscan` service. Its data lives in `/var/lib/overscan`, readable only by that user. That includes `secret.key`, the key that encrypts the addresses in the database that can hold a source's credentials (D46): keep a copy of it apart from any backup of the database.

### Or with the Debian package

On Debian or Ubuntu, the release's package (`overscan_<version>_amd64.deb`, or `arm64`) does the same, with the binary in `/usr/bin` (D124):

    apt install ./overscan_<version>_amd64.deb

It adds the `overscan` user, writes `/etc/overscan/overscan.yaml` from the example if there is none, and enables and starts the service. It depends on ffmpeg, which apt installs and sets up first, so that the server finds it when it starts; to run without ffmpeg, use `install.sh --no-ffmpeg` instead. Use one way or the other: a server installed with `install.sh` is moved to the package by `./install.sh --uninstall` first, which keeps the configuration and the data.

## 3. Configure

Set the administrator's password, then sign in to the admin at `http://<container>:8080/admin/` and add your sources there:

    runuser -u overscan -- overscan admin password

A source declared in `/etc/overscan/overscan.yaml` (the file explains each setting) is imported into the database once, at the next restart, and the admin manages it from then on (D46); its entry can then be deleted, which takes its address out of the file. Until then the file stays readable only by root and the `overscan` group, since source addresses often hold credentials. To restart and follow the log:

    systemctl restart overscan
    journalctl -u overscan -f

Recordings (D89), once turned on in the admin's *Settings*, go in `/var/lib/overscan/Recordings`, the only place the service may write. To keep them elsewhere, such as a disk mounted at `/srv/recordings` and owned by the `overscan` user, set `recordings_dir: /srv/recordings` in the configuration file, let the service write there, and restart it:

    systemctl edit overscan     # add the two lines below, then save
    [Service]
    ReadWritePaths=/srv/recordings

    systemctl restart overscan

At its start, the server points the `recordings` source at the new folder. Recordings made before stay where they were: move their files into the new folder to keep them on the box's shelf. The admin still deletes them, from either folder.

## 4. Manage

Run Overscan's commands as its user. Debian containers may not have `sudo`; `runuser` is always there:

    runuser -u overscan -- overscan devices list
    runuser -u overscan -- overscan devices approve -label "Living room" 00:1A:79:XX:XX:XX
    runuser -u overscan -- overscan debug playlist

`overscan help` lists the commands, and `overscan help devices`, or any command with `-h`, says more about one.

[`guide.md`](guide.md) says where each task is done, and [`trying-it.md`](trying-it.md) walks through them.

## Backups

[`backup.md`](backup.md) says what to keep and how to restore it. In short, keep a copy of `/var/lib/overscan/secret.key` apart from everything else, and copy the database now and then:

    runuser -u overscan -- overscan backup

## Upgrading and removing

To upgrade, unpack the new archive and run its `install.sh`, or install the new package with `apt install`. Either keeps your configuration and data. Before a new version changes the database's schema, it copies the database into `/var/lib/overscan/backups/`, which lets you go back: [upgrading.md](upgrading.md) says how.

To remove Overscan, run `./install.sh --uninstall` from an unpacked archive, or `apt remove overscan` for the package. Both stop the service and remove it and the binary, and keep the configuration, the data and the `overscan` user. `apt purge overscan` removes the configuration file too. The data, with the database, the key file and the recordings, is never removed for you; once you no longer need it:

    rm -r /etc/overscan /var/lib/overscan   # configuration and data
    userdel overscan
    apt-get remove ffmpeg                   # if nothing else uses it
