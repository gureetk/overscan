# Trying Overscan

Overscan is in early development. This page says how to try what works so far with the channels of an M3U playlist, an Xtream server or a provider's Stalker portal: a MAG box with the first version of Overscan's own page, a Stalker client, such as a TV app with a Stalker portal option, and a player such as VLC with a debug playlist.

## 1. Run the server

Take the archive for your machine from the repository's Releases page (`linux_amd64` for 64-bit x86, `linux_arm64` for 64-bit ARM), unpack it, and write a configuration file, `overscan.yaml`. To try Overscan, run it as yourself, without `sudo`, with its data in a folder of your own:

```yaml
listen: ":8080"
data_dir: /home/you/overscan-data
```

`deploy/overscan.example.yaml` in the archive explains every setting. Overscan creates the data folder, readable only by the user that runs it, since it holds the admin token, the list of devices and `secret.key`, the key that encrypts the source addresses in the database (D46); the default, `/var/lib/overscan`, is for the service. If you back up the folder, keep the key apart from the database. Then start the server:

    ./overscan -config overscan.yaml

Run the commands below as the user that runs the server, with the same `-config` flag. To run Overscan as a service in a Proxmox container instead, see [`install-lxc.md`](install-lxc.md); the commands are then `runuser -u overscan -- overscan ...`.

## 2. The admin and a source

Set the administrator's password, which the command asks for twice without showing it:

    ./overscan -config overscan.yaml admin password

Then open `http://<server>:8080/admin/` in a browser and sign in as `admin` (D44).

On *Sources*, *Add a source* takes the playlist's address, or the path of a playlist file on the server, how many streams the account allows at once, and how often to read it. *Test* reads the address without saving anything. Overscan keeps the address encrypted and from then on shows only its host. It reads the source at once, and the log says `source refreshed` with the number of channels. Channels are numbered 1, 2, 3 and so on in the playlist's order; the source's *Channel numbers* setting can use the playlist's own numbers instead (D40).

An Xtream server is a source too (D50). Choose *Xtream server* as its type, and enter the server's address, only its start such as `http://provider.example:8080`, with the username and the password. *Test* signs in and says what the server tells about the account: until when it runs, and how many streams it allows at once. Overscan keeps to the lower of that and the source's own *Streams at once*. A channel's address, with the login in it, is made when the channel plays, and Overscan always proxies it, so the login never reaches a device.

A provider's Stalker portal is a source too (D76), read as the box that the provider registered for your subscription. Choose *Stalker portal* as its type, and enter the portal's address as the provider gave it, such as `http://portal.example/c/`, and the box's MAC address. If the provider also checks the box's serial number, device IDs, signature or model, or asks for a login, enter them under *The box's other details*; leave them empty otherwise. Overscan signs in with this MAC address only. *Test* signs in and says whether the portal takes the box, until when the account runs, and how many channels it lists. Overscan reads the portal's genres and channels on the source's schedule, and after them its guide, when it was last read 6 hours ago or more. When a channel plays, Overscan asks the portal for its link, keeps the box's session alive while it plays, and keeps to the source's *Streams at once*, one by default, as a box plays one stream at a time: a device that changes channel stops its stream before the next starts. The portal sees one box, Overscan; a box or app that signs in to the portal with the same MAC address signs Overscan out until it signs in again.

A portal's films and series appear on the boxes' *Movies* and *Series*, each of the portal's categories a shelf of its own (D77). Overscan reads the categories when a box first opens *Movies* or *Series*, and a shelf's titles a page of 14 at a time as boxes scroll through it, at most four pages at once, each kept a day; the log says `read a page of a portal's shelf` for each. A shelf's count shows once a part of it has been read. A portal without a series module lists its series among its films, and so does Overscan: they open as series from *Movies*. Search finds what has been read so far. A film or an episode plays through Overscan, which asks the portal for its link as it starts, and again if the portal stops taking it when the player seeks.

Overscan reads each source's guide after its channels (D51): an Xtream server's own, a Stalker portal's own, or the XMLTV guide that a playlist names in `url-tvg`. The log says `guide read` with how many channels it covers and how many programmes it holds, and so does the source's card on *Sources*. The log line also says how many channels the guide itself lists (`guide_channels`) and how many of the source's it matched by name (`by_name`). If `guide_channels` is not much more than `channels`, the provider's guide has nothing for the source's other channels. A Stalker client shows the guide; Overscan's own page will once its new interface arrives.

A Jellyfin server is a source of films and series (D66). In Jellyfin's dashboard, under *API Keys*, make a key for Overscan. Then on *Sources* choose *Jellyfin server*, and enter the address you open Jellyfin at, such as `http://192.168.1.20:8096`, and the key. With a *User*, boxes see the libraries that this Jellyfin user sees; without one, every library of films and shows on the server. *Test* says which libraries boxes would see. A Jellyfin source has no channels: Overscan checks on its schedule that the server answers, and the card says when it last did. Its films and series appear on the boxes' *Movies* and *Series*, a shelf for each library, and a film added to Jellyfin shows up within about a quarter of an hour of a box looking. Only Overscan needs to reach the server: films and pictures come to the boxes through it, and the key never leaves it.

A Plex Media Server is a source of films and series too (D72). Overscan needs the token of a Plex account that sees the libraries: Plex's support pages explain how to find it, and in Plex Web a film's *Get Info → View XML* shows it as `X-Plex-Token` in the address. On *Sources* choose *Plex server*, and enter the server's address on your network with its port, such as `http://192.168.1.20:32400`, and the token. *Test* says which libraries boxes would see: the libraries of films and of shows that the account sees, each a shelf on the boxes' *Movies* and *Series*. As with Jellyfin, only Overscan needs to reach the server, and the token never leaves it. Overscan does not ask Plex to transcode: a film whose sound the box cannot play goes through Overscan's ffmpeg, and one whose video it cannot play plays as it is, unless *Settings → Transcoding* lets Overscan's own ffmpeg transcode it, as below.

A folder on the server is a source of films and series too (D67). Choose *Folder*, and enter the folder's absolute path, such as `/srv/media/films`; *Test* says how many films and series Overscan finds in it. Overscan reads the names of files and folders, the `.nfo` files and the pictures beside them as Kodi, Jellyfin and Plex lay them out, and never changes them; [library-folders.md](library-folders.md) says how, and how to let the service read the folder. Each folder is a shelf on the boxes' *Movies* and *Series*.

A playlist source can become an Xtream source of the same server, and keep its channels with their numbers, names and other changes. Back up the data folder first. Then on the source's *Change* page choose *Xtream server* and enter the server's address, username and password. At the next read the log should say `source refreshed` with `added=0`. Changing the type back to a playlist works the same way.

A source can also come from the configuration file, as in earlier versions:

```yaml
sources:
  - name: main
    type: m3u
    url: http://provider.example/get.php?username=USER&password=PASS&type=m3u_plus
```

An Xtream source there has `type: xtream`, the server's address as `url`, and `username` and `password`; a Stalker portal has `type: stalker`, the portal's address as `url`, `mac`, and where the provider checks them `serial`, `device_id`, `device_id2`, `signature`, `model`, `username` and `password`; a Jellyfin source has `type: jellyfin`, the server's address as `url`, `api_key`, and `user` if boxes should see only that user's libraries; a Plex source has `type: plex`, the server's address as `url`, and `token`; a folder has `type: folder` and its path as `url`. At its next start Overscan imports the source into the database, once, and logs `imported a source from the configuration file`. From then on the admin manages it, so its entry can be deleted from the file, which takes the address with its credentials out of it. Until then, keep the file to yourself (`chmod 600 overscan.yaml`).

*Live TV* arranges what devices see of the channels: groups, names, numbers and hidden channels, which stay as you set them when sources are read again (D47). *Movies* and *Series* do the same for films and series: hide a shelf, rename it or move it up and down, and hide titles, by ticking them (D79). *Devices* approves boxes and players, as the commands below do.

## 3. A MAG box

A MAG box loads its portal address as a web page, which is the box's whole interface. Overscan serves its own, in Overscan's own theme (D52): Home, Live TV with a small player, the guide, films and series, search and settings.

1. On the box, open *System settings → Servers → Portals* and set *Portal 1 URL* to `http://<server>:8080/c/`. A box already set to `http://<server>:8080/stalker_portal/c/` works as it is. Save, then choose *System settings → Restart Portal*. Menu names can differ between firmware versions.
2. The box shows *Waiting for approval* with a six-digit code, and the server log shows `MAG page requested`, then `device asks to be paired` with the same code. Approve the box with the code:

       ./overscan -config overscan.yaml devices approve -label "Living room" 123456

   The admin's *Devices* page can approve it with the code too. A few seconds after the approval, the box shows Home. The box is now paired: it keeps a secret, so that its MAC address alone is not enough to get in, and it comes back after a restart without another approval. `devices list` shows each box's pairing.
3. On Home, the arrows and OK open *Live TV*, the *TV Guide*, *Movies*, *Series*, *Search* or *Settings*. Digits go to a channel by its number from any screen that shows channels.
4. *Live TV* lists the groups on the left, with *Favourites*, *Recently Watched* and *All Channels* first, and the channels of a group with what is on now. Left goes to the groups. OK plays the channel in the small player on the right, beside its programme and what comes next; OK again goes full screen.
5. Full screen, the channel keys change channel, Info shows or hides the banner, and OK or Back returns to Live TV. The banner shows what is on, how good the picture is, and the sound track when there are several. The colour keys act on what has focus: red adds the channel to the favourites or takes it out, yellow sorts the list by name or number (while watching, it picks the sound track), blue searches (while watching, it opens the guide). Green is kept for catch-up.
6. *Movies* and *Series* show the films and series of the Xtream, Jellyfin and Plex sources and the folders, on shelves: *Continue Watching*, *Recently Added* and the sources' categories. The first time, Overscan reads each source's whole list, which can take a moment (D63); the admin's *Sources* page then says how many it read. OK opens a title's details, with *Play* or *Resume*; a series offers its next episode and *Episodes*. While a film plays, Left and Right go back 10 seconds and on 30, the channel keys 5 minutes, OK pauses, and Back stops it; the next episode follows the one that ends. How far you got is kept for the household's account, so another box can go on from there (D65). The first time a film or an episode plays, Overscan reads what its file holds, which can take a second or two. One whose sound the box cannot play, such as DTS or Dolby TrueHD, comes through ffmpeg, which copies the picture and converts the sound to Dolby Digital (D68): the log says `a title plays through ffmpeg`, and the admin's *Overview* marks it *Converted*. A seek in it takes a moment longer and lands a little before where you asked, on the nearest picture that the stream can start from; Yellow names its sound tracks, with the ones converted. Blue lists the subtitles: those beside the file in a folder, or that Jellyfin has, and those in the file itself when the box plays the file as it is (D69). A Jellyfin film whose picture the box cannot play, such as one in 4K or AV1, comes through Jellyfin's own transcoder (D71): the log says `a title plays through its source's server`, and the admin's *Overview* marks it *Transcoded*. Its stream carries one sound track at a time, so picking another with Yellow starts it again from where it was. From any other source, such a film plays as it is, unless *Settings → Transcoding* lets this server's ffmpeg transcode it, one, two or three titles at a time (D73): it then comes as H.264 at up to 1080p, with a high dynamic range mapped to the standard one, the log says `a title plays through ffmpeg, which transcodes its video for the box`, and the *Overview* marks it *Transcoded*. That needs a fast processor, 4K above all: the log says how fast each transcode went, and warns `ffmpeg transcodes a title more slowly than the box plays it` when the box will stop to wait. While as many titles as the setting allows are transcoded for other boxes, a box that asks for one more is told that the server is busy.
7. *Settings → Appearance* chooses Home's look, *Simple* or *With a Picture*, its picture, the accent colour and the clock. *Settings → Channel Numbers* can number the favourites, or every group, from 1 on this box; a number you type then picks the channel of that number in the list on screen (D56). *Settings → Groups and Shelves* chooses whether Live TV shows a group's channels, and Movies and Series a shelf's titles, as you move, or only when you press OK or Right (D62). Each box keeps its own settings, on the server.
8. *Settings → Playback Check* learns which kinds of film the box plays (D70). With the sound up, OK starts it: the box plays 23 short clips, one after the other, each with a moving test pattern and a tone that beeps each second, and asks after each whether the picture moves or the tone sounds: OK for yes, Red for no, Yellow for not sure, Left to play it again. A clip that the box does not start counts as one it does not play. The first time, the server makes the clips with ffmpeg, which takes about a minute (`made the playback check's clips` in the log). At the end, the box says what it plays and what it does not, the log says `a box's playback check found what it plays`, and the admin's *Devices* page lists it under *What the boxes play*. From then on, a film whose sound or file the box does not play goes through ffmpeg, and one that it does play plays as it is; a box that was never checked plays as the last box of its model that was.

If a channel shows a black screen, try another, and note what the server log says when you press OK.

### The parental PIN

On the admin's *Live TV* page, lock a group from its header, or tick channels and choose *Lock*, then set a PIN of 4 to 8 digits on the *Settings* page. The box then asks for the PIN before it plays a locked channel, and keeps it for 10 minutes; five wrong PINs make it wait five minutes. Stalker clients do not see locked channels at all. Without a PIN, locks do nothing (D55).

### Stream repair

A channel that stutters, skips or jumps back can be set to be repaired: on the admin's *Live TV* page, click the channel's name and set *Stream repair* to *Repair* in the panel beside the list, or tick several channels and choose *More*, then *Repair*. *Repair every channel*, on the *Settings* page, repairs every channel but those set to *Don't repair* (D58). Its stream then goes through ffmpeg, which copies it into a new stream with its timestamps rebuilt, without re-encoding it (D57). The channel starts a second or two later than before. The server needs ffmpeg: `install.sh` installs it, and on another system install it with the package manager, or set its path as `ffmpeg` in the configuration file. The server log says `ffmpeg found to repair streams with` at start, and `repairing a stream with ffmpeg` each time a repaired channel plays; the admin's *Overview* marks it *Repaired*. A stream that ffmpeg cannot read plays as it is, and the log says why. A channel whose address ends in `.m3u8` plays as HLS, which repair cannot reach, unless *Repair HLS streams* is on, on the *Settings* page: a repaired channel's HLS then comes to the box as one stream that the server makes from it, in its best quality up to 1080p, and goes through ffmpeg (D61).

## 4. A Stalker client

This is for an app with a Stalker portal option, which talks to the server's API itself. Give it a MAC address of its own: the server refuses the MAC address of a box paired with the MAG page.

1. In the client, add a Stalker portal with the address `http://<server>:8080/stalker_portal/c/` and any MAC address the client offers.
2. The first connection is refused, and the server log shows `new device waiting for approval` with the MAC address.
3. Approve it:

       ./overscan -config overscan.yaml devices approve -label "Test client" 00:1A:79:XX:XX:XX

4. Connect again. The client lists the channels by group and plays them through the server.
5. If the server has a source of films and series (section 2), the client's films are the box's *Movies* and its series the box's *Series*, by the same categories. The first time the client asks, the server reads the source's list, which can take a few seconds. A series' seasons are read when the client opens it. Films and episodes play as they are, through the server, with seeking; unlike on the box, nothing is converted or transcoded for a client (D75), so a client whose player cannot decode a file's sound or picture plays it without them.

`devices list` shows every device and its state.

## 5. The debug playlist

    ./overscan -config overscan.yaml debug playlist

prints an address. Open it in VLC (*Media → Open Network Stream*) to get the household lineup as a playlist; each entry plays through the server. The address holds the admin token, so keep it to yourself.

## 6. Checking a channel

For a channel that misbehaves,

    ./overscan -config overscan.yaml debug channel 42

checks channel 42: what its source answers, and what a player gets through the server, which it plays for 30 seconds (`-for 2m` plays it longer). The report says what the source sends, whether the stream ends early, which streams it holds, whether packets go missing, and how its clock runs: whether it jumps, how far, and whether the pictures after a jump back repeat ones already sent. It names no addresses, so it can be shared. The check takes one of the source's connections, which the server does not count, so on a source that allows only one, stop playing it on the box first. A channel set to be repaired goes through ffmpeg in the check too; `-repair on` or `-repair off` decides that for the check alone, so the two reports can be compared.

## What to report

What the box or client shows and plays, how quickly the box answers the remote, anything that fails, and the server log lines around it. The admin's *Settings* page offers a support bundle, a zip file with the version and settings, the state of the sources, lineup and devices, and the log's last 1,000 lines, without addresses, logins or MAC addresses; read it before you share it. The log never shows source addresses, but it does show MAC addresses and channel names. A channel whose source serves HLS from an address that does not say so is relayed: the log says `relaying an HLS stream as one continuous stream` when it starts, and `HLS relay ended`, with a reason, if the source stops it. The log also says when a source ends a stream that a player is still watching, when a player reconnects to a stream by itself, and when a source sends a stretch of a stream again, which the server leaves out (`the source sent a stretch again; the hub left it out`), and when ffmpeg could not repair a stream (`ffmpeg could not repair a stream, so it plays as it is`). For a channel that misbehaves, add its report from `debug channel` (section 6). Requests that Overscan does not support yet go to a capture log, which shows what clients ask for: the admin's *Capture log* page lists them by kind, each with its last example, redacted. The admin's *Overview* shows what is playing and on which device, which helps to match a problem on the box to the log.
