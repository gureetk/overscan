<!-- SPDX-License-Identifier: Apache-2.0 -->

# Xtream API notes

What a client sees when it reads a provider through the Xtream API: the account call, the channel lists, the guide and the stream addresses. Overscan reads this API from M2 for live TV and the guide, and will serve it from M6 (D27).

**Sources.** Open-source projects and public write-ups, cited below by number. Each statement carries a mark from the [clean-room policy](../clean-room.md): **Documented** (a named public source says so), **Observed** (seen by a contributor, who says with what) or **Assumed** (inferred, and a hypothesis until a real panel confirms it). Nothing here is Observed yet.

1. [`@iptv/xtream-api`](https://github.com/ektotv/xtream-api) 1.4.1, a TypeScript client (MIT): its response types, its request code and its fictional fixtures.
2. [`go.xtream-codes`](https://github.com/tellytv/go.xtream-codes), a Go client (MIT): its lenient field types, and the replies of four panels recorded in 2018 in its test data.
3. [`kludgarr/go.xtream-codes`](https://pkg.go.dev/github.com/kludgarr/go.xtream-codes) v0.1.2 (2026), a fork of 2 (MIT): its documentation of failure signals and field quirks.
4. [tuliprox](https://github.com/euzu/tuliprox), a proxy that reads and serves the API (MIT): its documentation, and the fields of its model of the API's live streams and films (read at v3.3.204), described here in our words.
5. [Discussion 434](https://github.com/AndreyPavlenko/Fermata/discussions/434) in the Fermata project on GitHub: a community list of the calls, corrected in its replies (2024 to 2026).
6. UniPlayer's public [catch-up specification](https://uniplayer.net/docs/specs/catchup/).
7. Two threads in the Channels community forum (2026): [redirects on stream links](https://community.getchannels.com/t/mpeg-ts-streams-broken-not-following-302-redirects/46345) and [status 461](https://community.getchannels.com/t/stream-fails-in-channels-app-and-chrome-but-works-in-vlc/46284).
8. The XMLTV project's description of its format, [`xmltv.dtd`](https://github.com/XMLTV/xmltv/blob/master/xmltv.dtd).

**Clean-room status.** The API comes from proprietary IPTV panel software: Xtream Codes, and later panels that keep its API, such as XUI. No panel code was used, and no write-up made from panel code. Some "API documentation" found online says it was produced by analysing decoded panel source; the clean-room policy excludes it, and nothing here comes from it.

## 1. Where the API lives

- A provider gives a server address (`http://host:port`, sometimes `https`), a username and a password. Clients add paths to that address, and [1] drops a trailing slash first. **Documented** [1, 2].
- Every call carries the username and password as query parameters, and every stream address carries them as path segments. **Documented** [1, 2]. They end up in logs, proxies and histories unless a client keeps them out. Overscan redacts them everywhere and always proxies these streams (plan, section 6.2).
- Both clients put the username and password into addresses unescaped. **Documented** [1, 2]. **Assumed:** percent-encode them; a password containing `/` cannot work in the path form at all.
- Calls are `GET` requests. **Documented** [1, 2]. Some providers accept only certain user agents, so a client sends its user agent on every request, the first included. **Documented** [3]. tuliprox lets its users choose the user agent it sends. **Documented** [4].
- [3] treats as a failure any status of 400 or more, and any 2xx answer to a JSON call whose body is empty, begins with `<` (an HTML page) or is one of a few bare words. **Documented** [3].

## 2. The account: `player_api.php` with no action

`/player_api.php?username=U&password=P` answers `{"user_info": {...}, "server_info": {...}}`. **Documented** [1, 2, 5].

`user_info`:

- `auth` is `1` when the login is good. **Documented** [1]. An answer without `user_info` is a failed login. **Documented** [3]. **Assumed:** treat any other `auth` as a failed login too.
- `status` is `"Active"` for a working account, and tuliprox, serving the API, also uses `"Banned"`. **Documented** [1, 2, 4]. **Assumed:** anything but `"Active"` cannot play.
- `exp_date` is Unix seconds as a string, or `null` for an account that does not expire. **Documented** [2].
- `max_connections` is how many streams may run at once, and `active_cons` how many run now. **Documented** [1, 2]. Both are strings in [2]'s recordings; [1] types `active_cons` as a number.
- `allowed_output_formats` lists the stream formats the account may use, from `"m3u8"`, `"ts"` and `"rtmp"`. **Documented** [1, 2].
- Also `username`, `password`, `message` (free text from the provider), `is_trial` (`"0"` or `"1"`) and `created_at` (Unix seconds). **Documented** [1, 2].

`server_info`:

- `timezone` is an IANA name such as `Europe/London`. `time_now` is the panel's wall clock in that zone, and `timestamp_now` is Unix seconds. **Documented** [2]: in all four recordings, `time_now` is `timestamp_now` shifted by that zone's offset.
- `url`, `port`, `https_port`, `rtmp_port` (strings) and `server_protocol` name the panel's own address. Both clients build stream addresses from the address the user entered instead. **Documented** [1, 2].
- [1] also lists `xui` (true on an XUI panel), `version` and `revision`, which the 2018 recordings [2] lack. **Documented.**

## 3. Live TV

`&action=get_live_categories` answers `[{"category_id", "category_name", "parent_id"}]`. `category_id` is a string; `parent_id` is a number, `0` in every recording. **Documented** [1, 2].

`&action=get_live_streams`, optionally with `&category_id=N`, answers every live channel in one array. **Documented** [1, 2, 5]. Several thousand entries are normal [2]. Each entry:

- `stream_id`: a number, unique in every recording [2]. It identifies the channel and goes into its stream address. **Documented** [1, 2].
- `num`: the panel's order, counting from 1 with no repeats in the recordings. **Documented** [2].
- `name`, and `stream_icon`, a logo address that is often empty. **Documented** [1, 2].
- `stream_type`: `"live"`. The recordings also hold `"created_live"` and `"radio_streams"`. **Documented** [2]. **Assumed:** `"radio_streams"` entries are radio stations.
- `epg_channel_id`: the channel's id in the provider's guide, or `null`. Most channels in the recordings had none. **Documented** [1, 2].
- `category_id`: a string, and sometimes `null`. **Documented** [2]. [1] also lists `category_ids`, an array. **Documented.**
- `tv_archive` (`0` or `1`) and `tv_archive_duration` (days, as a number or a string) describe catch-up (section 6). **Documented** [1, 2, 6].
- `added` (Unix seconds as a string) and `custom_sid` [1, 2], and `thumbnail` [1]. **Documented.**
- `direct_source`: usually empty. When set, it is an address that bypasses the panel; tuliprox ignores it by default. **Documented** [2, 4].
- `is_adult`: `1` for a channel for adults, as a number or its string; `0`, or no such field, otherwise. **Documented** [4]: tuliprox's model of a live stream reads it, a number that may come as a string, 0 where missing. The recordings [2] lack it. Overscan keeps it as the channel's mark for adults (D86).

## 4. Stream addresses

- Live: `/live/U/P/<stream_id>.<ext>`, where `<ext>` is one of `allowed_output_formats`. **Documented** [1, 2]. [1] uses the first allowed format when the one it wants is not allowed, and `ts` in place of `rtmp`. `.ts` is one continuous MPEG-TS stream and `.m3u8` an HLS playlist. **Assumed.**
- tuliprox can leave out `/live/` or the `.ts` extension for providers that need that, so not every panel takes the same form. **Documented** [4].
- Movies: `/movie/U/P/<stream_id>.<container_extension>`. Series episodes: `/series/U/P/<episode id>.<container_extension>`. **Documented** [1, 2].
- A stream request can answer `302` with an address on another host, often plain HTTP with a token in it, which then serves `video/mp2t`. **Documented** [7]. Follow redirects [3]. **Assumed:** the new address is short-lived and good for one play, so ask the panel again for each play.
- A refused stream can answer `461`, a status outside the standard set, for a bad login, an expired or disabled account, too many connections or a blocked address. **Documented** [7]. **Assumed:** `403` and other 4xx statuses mean the same.
- The account may run `max_connections` streams at once. **Documented** [1, 2]. **Assumed:** one more is refused rather than ending an older one; a connection still counts for a moment after it closes, so a quick change of channel can be refused once; API calls do not count. A client should close a stream's connection as soon as nobody watches it, and retry a refused start once after a short pause.

## 5. The guide

- `/xmltv.php?username=U&password=P` answers the provider's whole guide as XMLTV [8]. **Documented** [2, 4, 5]. Not every provider serves it, nor `get.php` (section 7). **Documented** [5]. **Assumed:** its channel ids are the ones in `epg_channel_id`.
- XMLTV times are `YYYYMMDDhhmmss`, optionally followed by a time zone; a time without one is UTC. **Documented** [8]. tuliprox can shift guide times, by an offset or to a named zone, for guides that do not match the viewer's time zone. **Documented** [4].
- `&action=get_short_epg&stream_id=N`, optionally with `&limit=M`, answers `{"epg_listings": [...]}` with the channel's next programmes, four when no limit is given. **Documented** [2, 5].
- `&action=get_simple_data_table&stream_id=N` answers the channel's whole guide in the same shape, and each entry adds `now_playing` and `has_archive` (`0` or `1`). **Documented** [1, 2, 5].
- An entry has `id`, `epg_id`, `channel_id` (the guide id), `lang`, `title` and `description` in Base64, `start` and `end` as `YYYY-MM-DD hh:mm:ss`, and `start_timestamp` and `stop_timestamp` as Unix seconds, either strings or numbers. **Documented** [1, 2, 3].
- The Base64 is usually padded; [3] accepts it with or without padding. **Documented** [3]. Decode leniently, and keep the text as it is if it does not decode. **Assumed.**
- `start` and `end` carry no zone. **Assumed:** they are in `server_info.timezone`, like `time_now`. Use the timestamps.
- **Assumed:** a client that needs the guide for many channels reads `xmltv.php` once, and calls `get_simple_data_table` only for channels that `xmltv.php` lacks, and only for channels it shows, spread out over time.

## 6. Catch-up

- A channel with `tv_archive` set keeps `tv_archive_duration` days. **Documented** [6].
- The address is `/timeshift/U/P/<minutes>/<YYYY-MM-DD:HH-MM>/<stream_id>.ts`, or `.m3u8`. **Documented** [1, 6]. tuliprox also passes a query form, `/streaming/timeshift.php`. **Documented** [4].
- The start time's zone is unsettled: [6] says it is the panel's own time zone, while [1] writes UTC. Overscan uses the panel's own, `server_info.timezone`, or UTC where it gives none, and a source's setting can name another (D81). **Assumed** until a real panel shows which.
- `<minutes>` is how long to play from the start. **Documented** [1, 6]. Overscan asks for the minutes that are left of the programme, rounded up, from the whole minute at or before the time a player asks for. [catchup-notes.md](catchup-notes.md) describes the archives of playlists.

## 7. Movies, series and the playlist

Overscan mirrors live channels, and reads films and series when a device asks for them, one list of each at a time (D63).

- `&action=get_vod_categories` and `get_series_categories` answer like `get_live_categories`. **Documented** [1, 2].
- `&action=get_vod_streams`, without `category_id`, answers every film in one list. An entry has `stream_id` (a number), `name`, `stream_icon`, `rating` (out of 10, as a string, sometimes empty), `rating_5based`, `added` (Unix seconds as a string), `category_id` (a string), `container_extension` (such as `mp4`), `stream_type` (`"movie"`), `custom_sid` and `direct_source`. **Documented** [1, 2]. [1] also lists `title`, `year`, `genre`, `plot`, `cast`, `director`, `release_date`, `episode_run_time` (minutes) and `category_ids`, which the 2018 recordings [2] lack. **Documented** [1]. tuliprox's model of a film also reads `is_adult`, as for a live stream (section 3). **Documented** [4].
- `&action=get_series`, without `category_id`, answers every series in one list. An entry has `series_id`, `name`, `cover`, `plot`, `cast`, `director`, `genre`, `releaseDate`, `last_modified` (Unix seconds as a string), `rating`, `rating_5based`, `backdrop_path`, `youtube_trailer`, `episode_run_time` (minutes, as a string) and `category_id`. In the recordings, `category_id` and `stream_type` can be `null`, and `backdrop_path` is a list of addresses, an empty string or `null`. **Documented** [2]. [1] adds `title`, `year`, `release_date` and `category_ids`. **Documented** [1].
- `&action=get_vod_info&vod_id=N` answers `{"info": {...}, "movie_data": {...}}`. `info` holds `name`, `o_name`, `cover_big`, `movie_image`, `releasedate`, `director`, `actors`, `cast`, `description`, `plot`, `age`, `mpaa_rating`, `country`, `genre`, `backdrop_path`, `duration_secs`, `duration` (`HH:MM:SS`) and `rating`, among others; `movie_data` holds `stream_id`, `name`, `added`, `category_id` and `container_extension`. **Documented** [1, 2]. A title without details can answer `"info": []` instead of an object. **Documented** [3].
- `&action=get_series_info&series_id=N` answers `{"seasons": [...], "info": {...}, "episodes": {...}}`. `info` is as in `get_series`. A season has `season_number`, `name`, `overview`, `cover` and `cover_big`. `episodes` is keyed by season number, and an episode has `id` (digits, as a string), `episode_num`, `title`, `container_extension`, `season`, `added` and `info`, with `plot`, `movie_image`, `duration_secs` and `duration`. **Documented** [1, 2]. Key names differ between providers, such as `releaseDate` and `release_date`. **Documented** [3]. **Assumed:** a server may send `episodes` as a list of lists, one a season, or as one list, an episode's `info` as `[]`, and the empty list or `null` for a title it does not know; Overscan reads all of these.
- A film plays at `/movie/U/P/<stream_id>.<container_extension>` and an episode at `/series/U/P/<id>.<container_extension>` (section 4). **Documented** [1, 2].
- `/get.php?username=U&password=P&type=m3u_plus&output=ts` answers the catalogue as an M3U playlist, with the same login as the API. **Documented** [4, 5]. **Assumed:** its links are the stream addresses of section 4, so the playlist and the API describe the same channels.

## 8. Field types

Panels disagree about types, and clients cope [2, 3]:

- numbers arrive as numbers or as strings, and some as empty strings or `null`;
- flags as `0` and `1`, as `"0"` and `"1"`, or as `true` and `false`;
- timestamps as numbers or strings of Unix seconds;
- a list sometimes as a single string.

Read every field leniently, and treat a value that cannot be read as missing rather than failing the whole record. **Documented** [3].

## 9. Being a polite client

No public source gives request limits. **Assumed**, and Overscan's own rule: one request at a time per provider; the account, the categories and the channel list once per refresh; a growing pause after errors; and never a crawl of titles one by one (plan, section 6.2). Films and series follow the same rule (D63): a list of each when a device first asks for it, and again only when a device asks once it is a day old; ten minutes' pause after a failed read; a title's details only when a device opens it.
