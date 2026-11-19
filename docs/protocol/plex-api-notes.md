<!-- SPDX-License-Identifier: Apache-2.0 -->

# Plex API notes

What Overscan asks of a Plex Media Server when it reads its films and series and plays their files, and what the server answers. The code is `internal/adapters/plex`.

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real server confirms it). Nothing here is Observed yet: no real Plex server has been asked. Plex Media Server is not open source, but Plex publishes its API:

1. Plex's own API documentation, [developer.plex.tv/pms](https://developer.plex.tv/pms/), published in September 2025 and read on 17 November 2026: signing in, the headers every request carries, answers in JSON, and how a client describes what it plays to the transcoder.
2. The OpenAPI description of the same API at [github.com/LukeHagar/plex-api-spec](https://github.com/LukeHagar/plex-api-spec) (MIT; the description itself under Apache-2.0), read at its commit of 17 November 2026: the paths, their parameters and the fields of their answers. It names the operations used here (`getServerInfo`, `getSections`, `getLibrarySectionsFallback`, `listContent`'s `/library/sections/{sectionId}/all`, `getMetadataItem`, `getAllItemLeaves`, `transcodeImage`, `getMediaPart`, `getStream`).

Only behaviour is taken from them, in our own words.

## 1. Signing in

- Every request carries the token in the `X-Plex-Token` header. Any `X-Plex-` header may also go in the query instead, which is how the addresses of files, pictures and subtitles carry it. **Documented** (1, "Authenticating with Plex"; 2, the introduction).
- A request should also say which client sends it: `X-Plex-Client-Identifier`, a lasting ID of the client, is "typically required", with `X-Plex-Product` and `X-Plex-Version`. Overscan sends `overscan`, `Overscan` and its version. **Documented** (1).
- Tokens come from plex.tv, where a client can also sign a user in with a PIN. Overscan does not sign in: the admin pastes the token of an account that sees the libraries, which Plex's support pages explain how to find. **Documented** (1, "Authenticating with Plex").
- The server answers 401 to a missing or wrong token. **Assumed.**

## 2. The server

- `GET /` describes the server; with `Accept: application/json`, the answer is JSON, as every answer below is: `{"MediaContainer": {…}}`. Overscan reads `friendlyName`, `version` (such as `1.42.2.10156-f737b826c`) and `machineIdentifier`, and takes an answer without the last two as one from something other than a Plex server. **Documented** (1, "Response format"; 2, `getServerInfo`).

## 3. Libraries

- `GET /library/sections/all` lists the libraries: `Directory` entries with `key` (digits), `title` and `type` (`movie`, `show`, `artist` for music, `photo`). Overscan keeps those of type `movie` and `show`. **Documented** (2, `getSections`).
- `GET /library/sections` gives the same list to an account that is not the server's owner, and on servers older than the documented path; Overscan asks there when the first answers 404 or 400. **Documented** (2, `getLibrarySectionsFallback`); the older servers are **Assumed**.

## 4. Films and series

- `GET /library/sections/<key>/all?type=1` lists a library's films and `type=2` its shows. `X-Plex-Container-Start` and `X-Plex-Container-Size`, as query parameters, page the list; the answer's `size`, `totalSize` and `offset` say where a page is. Overscan asks for 500 at a time and stops at `totalSize`, at an empty page, or, without `totalSize`, at a short page. **Documented** (2, the section's `all`, and the paging parameters).
- Each entry of `Metadata` is a title. Overscan reads `ratingKey` (its key, digits), `type`, `title`, `year`, `summary`, `contentRating`, `audienceRating` and `rating` (out of 10), `duration` (milliseconds), `originallyAvailableAt` (a date), `addedAt` (Unix seconds), `thumb` and `art` (the paths of its pictures), and the tags in `Genre`, `Role`, `Director` and `Country` (each a `tag`). **Documented** (2, `Metadata`, `Tag`).
- A film's `Media` are its versions, each with a `container` and `Part` entries; a part's `key` is the path that sends its file, such as `/library/parts/12/1700000000/file.mkv`. Overscan plays the first part of the first version. **Documented** (2, `Media`, `Part`).
- `GET /library/metadata/<ratingKey>` gives one title with all its tags and, in each part, its `Stream` entries (5, 7). **Documented** (2, `getMetadataItem`).
- A number may come as a string, and a yes or no as `1` or `0`; Overscan takes both. **Assumed.**

## 5. Episodes

- `GET /library/metadata/<show>/allLeaves` lists a show's episodes, paged as above: `parentIndex` is the season's number, `index` the episode's, `parentTitle` the season's name, `thumb` a still from it. **Documented** (2, `getAllItemLeaves`, `Metadata`).
- An episode without `Media` has no file, and Overscan leaves it out. **Assumed.**

## 6. Pictures

- `GET /photo/:/transcode?url=<path>&width=<w>&height=<h>&minSize=1&upscale=1` sends one of the server's pictures as a JPEG, scaled to fill `w` by `h`. Overscan keeps a picture's path, never an address with the token, and adds the token only when its artwork cache fetches the picture. **Documented** (2, `transcodeImage`); that the picture needs the token is **Assumed**.

## 7. Playing a file

- `GET <part key>` (`/library/parts/<id>/<changestamp>/<name>`) sends a part's file as it is. The changestamp only busts caches and the name only gives the extension, but Overscan uses the key exactly as the server gives it. **Documented** (2, `getMediaPart`). That it answers range requests, as a file server does, is **Assumed**.
- Overscan plays these through its hub, as it plays an Xtream source's and a Jellyfin server's films (D64): the address with the token stays on the server, and the box gets a signed link.

## 8. Subtitles

- A part's `Stream` entries with `streamType` 3 are its subtitles, with `id`, `codec` (such as `srt`, `ass`, `mov_text`, `pgs`), `languageCode` (three letters), `title`, `forced`, `hearingImpaired`, `default`, and `index`, the stream's place in the file. A subtitle file beside the video has a `key` of its own. **Documented** (2, `Stream`); that only a file beside the video has a `key` is **Assumed**.
- `GET /library/streams/<id>.<ext>?format=srt&encoding=utf-8` sends a subtitle file beside the video, converted to the format asked for; it answers 501 for a stream in the file. **Documented** (2, `getStream`). Overscan asks for SubRip, or SubStation Alpha or WebVTT for subtitles in those formats, and reads subtitles in the file with its own ffmpeg (D69). Subtitles made of pictures are left out.

## 9. Not used yet

- `GET /video/:/transcode/universal/start.<m3u8|mpd>` starts the server's transcoder for `path=/library/metadata/<ratingKey>`, from `offset` seconds, as HLS, DASH or, with `protocol=http`, one stream (Matroska). `X-Plex-Client-Profile-Extra` adds to the profile that decides what the server makes, such as `add-transcode-target(type=videoProfile&context=streaming&protocol=http&container=mkv&videoCodec=h264&audioCodec=aac,ac3)`. **Documented** (1, "Profile Augmentations"; 2, `startTranscodeSession`). Overscan does not ask a Plex server to transcode.
