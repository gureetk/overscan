# Transcoding

Overscan sends a channel as its source sends it, unless something says to make it anew: a device's or an account's output profile, or what the household sets on live TV, a group or a channel. It sends a film or an episode to a box as its source sends it, unless the box cannot play its video, or what the household sets on films and series, a shelf or a title says otherwise. This page says how to set that up, and how to let Overscan use the server's graphics. [guide.md](guide.md) says where each setting is; the decisions behind it are D73, D110, D146 to D150 in [decisions.md](decisions.md).

## Profiles

A profile says what a transcode makes. Overscan has three of its own, which never change: *source* (as the source sends it), *h264-720p* and *h264-1080p*. *Settings* → *Transcoding* → *Make a profile* makes others, of:

- a codec: H.264, which plays nearly everywhere, or HEVC, which takes about half the bit rate for the same picture, and far more of a processor to make;
- a largest size: the source's, or at most 1080, 720 or 576 lines; a picture is never made larger;
- a quality, high, medium or low, and, for a slow network, a highest bit rate;
- the pictures a second: as the source sends them, which makes a picture of each half of an interlaced picture (50 a second from a 25-a-second channel), or at most 30, which takes about half the work;
- deinterlacing, and HDR made SDR;
- the sound: the first track as AAC in stereo or as Dolby Digital, or every track as the source sends it.

Accounts and devices choose profiles on their own pages, as before.

## What is transcoded

Live TV as a whole (*Settings* → *Transcoding*), a group (its *Transcoding…* button on *Live TV*) and a channel (its panel, or *More* for the ticked channels) are each:

- *as needed*: a device's or account's profile decides, as before;
- *always*, with a profile: devices and players that would get the channel as its source sends it get it transcoded; a device or account whose own profile transcodes keeps its own, since it says what the device can take;
- *never*: every device gets it as its source sends it, whatever its profile.

The narrowest setting wins: a channel's over its group's, and a group's over live TV's. A channel's setting belongs to the channel at its source, so it holds in every lineup. Recordings and the debug playlist always get the source's stream.

Every transcode takes one of a number at a time, and a stream that finds none free plays as its source sends it; the log says so. In software, that number is *Settings* → *Transcode video on this server*, which films and series share. On graphics, it is the *Graphics* card's.

## Films and series

Films and series as a whole (*Settings* → *Transcoding* → *Films and series*), a shelf (its *Transcoding…* button on *Movies* or *Series*) and a title (tick it, then *Transcoding* in the bar) are each:

- *as needed*: a film or an episode whose video the box cannot play, as its playback check says, is transcoded to H.264: by a Jellyfin source's own transcoder where *Let Jellyfin transcode* is on, and else by Overscan's ffmpeg where *Transcode video on this server* or the graphics allow; as before;
- *always*, with a profile: the box gets it transcoded with the profile, whether or not it could play it as it is. Its sound goes as the profile says, as far as the box plays it: AAC where the box plays AAC, and else Dolby Digital;
- *never*: the box gets its video as its source sends it, even where it may not play it; the log says so. Its sound is still converted where *Convert sound a box cannot play* is on.

The narrowest setting wins: a title's over its shelf's, and a shelf's over that of every film and series. A series' episodes follow it. A title kept on a shelf that merges categories of several sources takes that shelf's setting.

Only the boxes get films and series transcoded. Players through the outputs, and Stalker clients, get them as their sources send them, as before.

A film transcoded on the graphics takes one of their number at a time; when they are full, it is transcoded in software where its number allows, and when it cannot start at all, the box says that the server is busy. Where the graphics fail to start a film, Overscan tries again decoding it in software, then transcodes it in software.

## Smoothness

A transcode that cannot keep up with its stream stutters on the screen for as long as it plays. The *Overview* shows how fast each one goes, as seconds of video made each second: about *1.00x* keeps up. Below that for half a minute, while the stream waits to go in, it shows *Falls behind*, and the log says `ffmpeg transcodes a live stream more slowly than it comes`. A smaller size, at most 30 pictures a second, H.264 rather than HEVC, or the graphics take less of the processor.

## Graphics

Where the server has graphics that VA-API drives, which covers Intel graphics from Broadwell on and AMD graphics, ffmpeg can transcode on them, which takes far less of the processor. NVIDIA graphics are not used yet.

When it starts, Overscan tests what the graphics can do, with pictures that ffmpeg makes and never a channel: which codecs they make, in which mode, and whether they keep to a bit rate; which codecs they decode; and whether they deinterlace, scale and map HDR10 to SDR. *Settings* → *Transcoding* → *Graphics* says what it found, step by step, and *Test again* tests again, after a change to the server. On the command line, `overscan gpu`, run as the user that the server runs as, does the same test and prints it.

Then:

- a profile whose codec the graphics make is transcoded on them, up to the card's number at a time, 2 at first;
- where they also deinterlace and scale, the pictures go through those filters on them; where they also decode the stream's codec, as its channel last showed it, the pictures stay on them from decoding to encoding;
- a live HDR10 picture is made SDR, where the profile says so and the graphics can, once its channel has shown that it is HDR10;
- graphics that keep no bit rate, as some do only in low-power mode, keep a constant quality, and a profile's highest bit rate holds only in software.

What the graphics cannot do, the processor does:

- a stream that finds the graphics busy is transcoded in software, where the number in software allows, and otherwise plays as its source sends it;
- a transcode that fails to start on the graphics tries again with decoding in software, then in software, then plays as its source sends it; the log says why, with ffmpeg's last words;
- a transcode that stops part way while the graphics decode starts again at once with decoding in software, and that channel is decoded in software until the graphics are tested again.

### Giving Overscan the graphics

The graphics are the render device `/dev/dri/renderD128` (or `renderD129`, and so on, with more than one). The `gpu` setting in the configuration file chooses one, or `off` turns the graphics off; `auto`, the default, takes the first that Overscan can open.

Overscan must be able to open it, and VA-API must have a driver for the graphics:

- **The driver.** On Debian and Ubuntu, `intel-media-va-driver` for Intel graphics from Broadwell on (`intel-media-va-driver-non-free` adds encoding on some older ones), or `mesa-va-drivers` for AMD graphics; the Debian package suggests them. On Intel graphics from Alder Lake on, encoding at a bit rate in low-power mode needs the GPU's HuC firmware, which the kernel loads from `firmware-misc-nonfree` (or the distribution's `linux-firmware`) where it is installed; without it, Overscan finds that the graphics keep no bit rate, and keeps a constant quality instead.
- **The group.** The user that runs Overscan must be in the group that owns the device, usually `render`.
- **The service's sandbox.** The systemd unit hides physical devices (`PrivateDevices=yes`). A drop-in, `overscan-gpu.conf`, gives the service the graphics' devices and the `render` group, and leaves every other device closed to it where systemd can filter devices (D149).

With the release archive, `./install.sh --gpu` does all three: it installs the drop-in as `/etc/systemd/system/overscan.service.d/gpu.conf`, adds the `overscan` user to `render`, and installs the driver for the render device's maker. Upgrades keep it; `./install.sh --no-gpu` removes it.

With the Debian package, which holds the drop-in as `/usr/share/overscan/gpu.conf`:

    sudo apt install intel-media-va-driver     # or mesa-va-drivers, for AMD graphics
    sudo usermod -aG render overscan
    sudo install -D -m 0644 /usr/share/overscan/gpu.conf /etc/systemd/system/overscan.service.d/gpu.conf
    sudo systemctl daemon-reload && sudo systemctl restart overscan

`apt purge overscan` removes it again.

The log then says `graphics tested for ffmpeg to transcode on`, with what they do; `no graphics for ffmpeg to transcode on` says why not. `runuser -u overscan -- overscan gpu` tests them again and prints it.

**In a Proxmox container**, the host passes the device to it first: [install-lxc.md](install-lxc.md#graphics) says how. **With Docker**, [install-docker.md](install-docker.md#graphics) does.

## When it goes wrong

[troubleshooting.md](troubleshooting.md) has the log's lines and what they mean.
