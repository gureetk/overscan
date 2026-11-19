<!-- SPDX-License-Identifier: Apache-2.0 -->

# Jellyfin API notes

What Overscan asks of a Jellyfin server when it reads its films and series and plays their files, and what the server answers. Overscan reads Jellyfin from M3 (D66); the code is `internal/adapters/jellyfin`.

**Sources.** Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real server confirms it). Nothing here is Observed yet. Jellyfin is open source, so its own code is the main source:

1. [Jellyfin](https://github.com/jellyfin/jellyfin) server (GPL-2.0), read at its commit of 16 November 2026: the API controllers (`Jellyfin.Api/Controllers`: `Items`, `TvShows`, `Videos`, `UserViews`, `Library`, `System`, `User`, `Image`, and from 17 November `Subtitle`), its media sources and streams (`MediaBrowser.Model/Dto/MediaSourceInfo.cs`, `MediaBrowser.Model/Entities/MediaStream.cs`), how it reads a token (`Jellyfin.Server.Implementations/Security/AuthorizationContext.cs`), its item model (`MediaBrowser.Model/Dto/BaseItemDto.cs`), how it fills an item in (`Emby.Server.Implementations/Dto/DtoService.cs`), how it tells films from other videos (`Emby.Server.Implementations/Library/Resolvers/Movies/MovieResolver.cs`), how it answers a query for items (`Jellyfin.Server.Implementations/Item/BaseItemRepository.TranslateQuery.cs`) and its JSON settings (`src/Jellyfin.Extensions/Json`); and, read at its commit of 16 November 2026 for transcodes on 17 November, how it streams and transcodes (`Jellyfin.Api/Helpers/StreamingHelpers.cs`, `Jellyfin.Api/Helpers/FileStreamResponseHelpers.cs`, `MediaBrowser.MediaEncoding/Transcoding/TranscodeManager.cs`, `MediaBrowser.Controller/MediaEncoding/EncodingHelper.cs`, `Jellyfin.Api/Controllers/HlsSegmentController.cs`). Only behaviour is taken from it, in our own words; no code.

## 1. Signing in

- A request carries the key in an `Authorization` header: `MediaBrowser Client="…", Device="…", DeviceId="…", Version="…", Token="<key>"`. The values may be URL-encoded; the server decodes them. **Documented** (1, `AuthorizationContext`).
- The server also takes the key as `ApiKey=<key>` in the query, always. The older `X-Emby-Token` header, `api_key=` and the `Emby` scheme work only while the server's *legacy authorization* setting is on, so Overscan does not rely on them. **Documented** (1).
- An API key is made by an administrator in the dashboard (*API Keys*). A request with an API key acts for no user: it can read every library, and it skips users' parental settings. **Documented** (1, `ItemsController.GetItems`: "if api key is used … user will be null").
- A request without a valid key answers **401**; one whose key may not do something, **403**. **Documented** (1, `[Authorize]` on the controllers) and **Assumed** for the codes' exact use.

## 2. The server

- `GET /System/Info` answers the server's `ServerName`, `Version` (such as `10.11.2`) and `Id`. It needs a key, so it checks one too. `GET /System/Info/Public` answers without a key. **Documented** (1, `SystemController`).
- JSON names are in PascalCase (`ServerName`, `Items`). IDs are 32 hexadecimal digits without hyphens. Enumerations are written as their names (`Movie`, `Virtual`, `movies`). Times are ISO 8601 in UTC, such as `2026-12-04T10:30:00.0000000Z`. **Documented** (1, `JsonDefaults`, `JsonGuidConverter`, `JsonDateTimeConverter`).

## 3. Users and libraries

- `GET /Users` lists the users, each with `Name` and `Id`; it needs an administrator's key. **Documented** (1, `UserController`).
- `GET /UserViews?userId=<id>` lists a user's libraries as items with `Id`, `Name` and `CollectionType`. `GET /Library/MediaFolders` lists every library, for an administrator's key. **Documented** (1).
- `CollectionType` is one of `movies`, `tvshows`, `music`, `musicvideos`, `trailers`, `homevideos`, `boxsets`, `books`, `photos`, `livetv`, `playlists` and `folders`. A library of films and shows mixed has none. **Documented** (1, `CollectionType`).
- Overscan reads `movies`, `tvshows`, `homevideos` and the mixed ones. A user's view of folders (`folders`), which a user can turn on in Jellyfin, holds what the libraries hold, so Overscan leaves it out. **Assumed** for which of them hold films and series, apart from what the next section says.

## 4. Films and series

- `GET /Items` with `ParentId=<library>`, `Recursive=true` and `IncludeItemTypes=Movie` (or `Series`) lists a library's films (or series), `Limit` at a time from `StartIndex`, sorted with `SortBy=SortName`. The answer is `{"Items": […], "TotalRecordCount": n, "StartIndex": s}`; a server may send fewer than `Limit`, so a client pages until it has `TotalRecordCount`. With `userId`, it lists only what that user may see. **Documented** (1, `ItemsController.GetItems`).
- Which type a library's films are: `Movie` in a library of films, and in a mixed one; `Video` in a library of home videos, so Overscan asks such a library for `IncludeItemTypes=Video`. **Documented** (1, `MovieResolver`).
- A title's extras, such as trailers and scenes from its making, are `Video` items with `ExtraType` set, and a query for items keeps them. Overscan leaves out an item with `ExtraType`. **Documented** (1, `BaseItemRepository.TranslateQuery`).
- `Fields=Overview,Genres,People,DateCreated,DateLastMediaAdded,ProductionLocations` adds the plot, genres, cast and crew, when the server added the title, when a series last gained an episode, and the countries where the title was made. `EnableImageTypes=Primary,Backdrop` and `ImageTypeLimit=1` keep the image tags to one of each. `Container` comes whatever the fields. **Documented** (1, `DtoService`).
- An item has `Id`, `Name`, `Type`, `ProductionYear`, `CommunityRating` (out of ten), `OfficialRating` (such as `PG-13`), `Overview`, `Genres`, `People` (each with `Name`, `Type`, such as `Actor` or `Director`, and `Role`), `RunTimeTicks` (in ticks of 100 ns), `DateCreated`, `DateLastMediaAdded`, `PremiereDate`, `ProductionLocations`, `Container`, `ExtraType`, `ImageTags` (by image type) and `BackdropImageTags`. What the server does not know is null or left out. **Documented** (1, `BaseItemDto`).
- `Container` is the file's format as ffprobe names it, which for some files is a list: `mov,mp4,m4a,3gp,3g2,mj2` for MP4, `matroska,webm` for Matroska. Overscan turns it into an extension (`mp4`, `mkv`). **Assumed**, from ffprobe's format names.
- `GET /Items?Ids=<id>` gives one title with the same fields. **Documented** (1).

## 5. Episodes

- `GET /Shows/<series>/Episodes` lists a series' episodes in order, with `IndexNumber` (the episode), `ParentIndexNumber` (the season), `SeasonName`, `Container`, `RunTimeTicks` and, with `Fields=Overview`, the plot. An unknown series answers **404**. **Documented** (1, `TvShowsController.GetEpisodes`).
- For an API key, the list includes *missing* episodes: ones the server knows of from metadata but has no file for, with `LocationType` `Virtual`. `IsMissing=false` leaves them out. **Documented** (1).

## 6. Pictures

- `GET /Items/<id>/Images/Primary?tag=<tag>` is a title's poster (an episode's still), and `/Items/<id>/Images/Backdrop/0?tag=<tag>` its first backdrop. `maxWidth` asks for a smaller one. These need no key. **Documented** (1, `ImageController`, which has no `[Authorize]` on them).

## 7. Playing a file

- `GET /Videos/<id>/stream.<ext>?static=true&ApiKey=<key>` sends the file as it is, and answers range requests. The extension is the file's own, or left out. `HEAD` works too. `mediaSourceId` picks another version of a title that has several. **Documented** (1, `VideosController.GetVideoStream`, "the original file will be streamed statically without any encoding").
- Overscan plays these through its hub, as it plays an Xtream source's films (D64): the address with the key stays on the server, and the box gets a signed link.
- The same address without `static=true` makes a stream that the server transcodes, in the container its extension names, such as `stream.ts` for MPEG-TS. The server's ffmpeg writes it to a file in its transcode folder, and the answer sends the file as it grows, without ranges (`Accept-Ranges: none`). **Documented** (1, `VideosController.GetVideoStream`, `FileStreamResponseHelpers.GetTranscodedFile`).
- The query says what to make. `VideoCodec` and `AudioCodec` are lists: the video is encoded in the first of its list, and a sound track in one of its list is copied, others encoded in the first that the server can encode. `AllowVideoStreamCopy=false` makes it encode the video even where it could copy it. `MaxWidth`, `MaxHeight`, `VideoBitrate`, `MaxVideoBitDepth`, `Profile`, `Level`, `MaxAudioChannels` and `TranscodingMaxAudioChannels` bound what it makes; `AudioStreamIndex` picks the sound track, by its index among the item's streams, which is ffprobe's for streams in the file; `StartTimeTicks` says where it starts, in units of 100 ns. **Documented** (1, `StreamingHelpers.GetStreamingState`, `EncodingHelper.TryStreamCopy` and `CanStreamCopyVideo`).
- The server names the stream's file after the item's file, the request's user agent, `DeviceId` and `PlaySessionId`. A request with the same four while that file is there joins its stream, wherever it got to, whatever else the request asks. **Documented** (1, `StreamingHelpers.GetOutputFilePath`, `FileStreamResponseHelpers.GetTranscodedFile`).
- When the last request for such a stream ends, the server stops its ffmpeg ten seconds later; `DELETE /Videos/ActiveEncodings?deviceId=<id>&playSessionId=<id>` stops a play session's at once. **Documented** (1, `TranscodeManager.OnTranscodeEndRequest` and `PingTimer`, `HlsSegmentController.StopEncodingProcess`).
- A request made with an API key brings no user, so no user's setting can forbid it to transcode. **Documented** (1, `TranscodeManager.StartFfMpeg`: the check applies to a request with a user).
- Overscan asks for a transcode of a film or an episode whose video the box cannot play (D71): `stream.ts` with H.264 in High profile, level 4.1, at 8 bits, at most 1920×1080 and 8 Mbit/s, video copy refused, the sound codecs that the box plays from MPEG-TS, Dolby Digital first if it plays that, at most six channels, the sound track the page asks for, the time it asks for, `DeviceId` `overscan-<device>`, and a new `PlaySessionId` for each request, which the hub asks the server to stop once the request ends.

## 8. Subtitles

- `GET /Items?Ids=<id>&Fields=MediaSources` gives the item's `MediaSources`, the first its main version, each with an `Id` and its `MediaStreams`. A stream has `Type` (`Video`, `Audio`, `Subtitle` and others), `Index`, `Codec`, `Language`, `Title`, `IsForced`, `IsDefault`, `IsHearingImpaired`, `IsExternal` (a file beside the video) and `IsTextSubtitleStream`. **Documented** (1, `DtoService`, `MediaSourceInfo`, `MediaStream`).
- `IsTextSubtitleStream` is false for subtitles made of pictures: Blu-ray's (`pgs`, `pgssub`, `sup`), DVD's (`dvdsub`, `vobsub`) and DVB's (`dvbsub`). **Documented** (1, `MediaStream.IsTextFormat`).
- `GET /Videos/<item>/<media source>/Subtitles/<index>/Stream.<format>` sends a subtitle track, in the file or beside it, in the format asked for, such as `srt`, `vtt` or `ass`; `startPositionTicks` would move it earlier. It asks for no key; Overscan sends one all the same. **Documented** (1, `SubtitleController.GetSubtitle`).
- Overscan lists a film's or an episode's text subtitles when the page asks to play it, asks for each in its own format (SubStation Alpha's as `ass`, WebVTT's as `vtt`, the others as `srt`), and moves them itself (D69).
