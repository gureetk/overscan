<!-- SPDX-License-Identifier: Apache-2.0 -->

# Stalker protocol notes

**Sources:** a Stalker client implementation written by the maintainer (its request code, response models, tolerant JSON decoding, the fake portal it is tested against, and that test suite), plus Infomir's public documentation and community documentation. The client's own notes say they come from community documentation and real-world clients, not official specs, and the client has only been tested against fakes. Treat everything below as "what a working client expects", not as verified ground truth.

**Clean-room status:** this describes the box and client side as observed from client code the maintainers wrote. No Infomir server code was used. See [../clean-room.md](../clean-room.md).

Where a section says "the client", it means that implementation. Behaviour is confirmed against a real box or portal only where a note says so and marks it **Observed**.

## 1. Where the API lives

- Users type `http://host/c/`, `http://host:port/stalker_portal/c/` or `http://host/portal.php`. The client strips `c/` to get a base, then tries, in order: what was typed, `<base>/server/load.php`, `<host>/portal.php`, and on a bare host `/stalker_portal/server/load.php`.
- All calls are `GET` with query parameters `type`, `action`, the action's own parameters, and `JsHttpRequest=1-xml`.
- Answers are `{"js": ...}`. Numbers often arrive as strings, an empty list can be `false`, and errors can be HTML. The server should emit strings for numbers, as Ministra-style portals do, and clients stay lenient.
- Lists are paged: `{"total_items":N,"max_page_items":14,"data":[...]}`.

## 2. What the server sees from a box

- `User-Agent: Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3`
- `X-User-Agent: Model: MAG250; Link: WiFi`
- These two come from clients that imitate a MAG250. A real box reports its own user agent (section 9).
- `Referer: <base>/c/`
- `Cookie: mac=<URL-encoded MAC>; stb_lang=en; timezone=<IANA zone>`
- `Authorization: Bearer <token>` after the handshake.
- Devices are keyed by the `mac` cookie. An unknown MAC gets HTTP 401. The `timezone` cookie decides the day for guide queries.

## 3. Session

1. `stb / handshake`, with `token=""` first, then a random 32-hex token plus `prehash=SHA1(token)` upper-case. Answer `{"js":{"token":"..."}}`. A new handshake issues a new token and invalidates the old one.
2. `stb / get_profile`, with `hd, ver, num_banks, sn, stb_type, client_type=STB, image_version, video_out, device_id, device_id2, signature, auth_second_step=0, hw_version, not_valid_token=0, hw_version_2, timestamp, api_signature, prehash`. Answer a profile object with `id`, `blocked` ("0"/"1"), `watchdog_timeout` (seconds), and optionally a renewed `token`, `expire_billing_date` or `tariff_expired_date`.
3. Optional `stb / do_auth` with `login, password, device_id, device_id2`. Answer `{"js":true}` or `false`.
4. `account_info / get_main_info` returns the account. The client reads the end date from `end_date`, else oddly from `phone`. Accepted date forms: Unix seconds, `yyyy-MM-dd HH:mm:ss`, `yyyy-MM-dd`, `March 12, 2027, 12:00 am`.
5. A call without a valid token must answer HTTP 401/403, or HTTP 200 with a body beginning `Authorization failed.`. The client then re-handshakes once and retries.
6. An unknown action answers HTTP 404. The client treats it as "this portal lacks the action".

## 4. Live TV

- `itv / get_genres` returns `[{"id","title","censored"}]`, with `{"id":"*","title":"All"}` first.
- `itv / get_all_channels` returns `{"js":{"data":[...]}}`. Channel fields: `id, name, number, cmd, logo, tv_genre_id, xmltv_id, censored`, and archive flags `tv_archive` / `enable_tv_archive` / `archive` with `tv_archive_duration` in hours (24, 168).
- Fallback: `itv / get_ordered_list` with `genre=*, p, sortby=number, fav=0, hd=0`.
- A channel's `cmd` looks like `ffrt http://host/ch/105_` or `ffmpeg http://...`.
- `itv / create_link` with `cmd, series, forced_storage, disable_ad, download, force_ch_link_check`. Answer `{"js":{"cmd":"ffmpeg http://.../x.ts?token=...","error":""}}`. Prefixes seen are `ffmpeg `, `ffrt ` and `auto `, and the client keeps only the last space-separated token. An `error` string such as `nothing_to_play` or `limit` means no link.
- Links are minted at play time and expire.

## 5. Guide and catch-up

- `itv / get_epg_info&period=<hours>` returns `{"js":{"data":{"<channelId>":[programme,...]}}}`. A programme has `id, name, descr, category, start_timestamp, stop_timestamp` (Unix seconds as strings) and `mark_archive`.
- `epg / get_simple_data_table` with `ch_id, date=YYYY-MM-DD, p` returns the paged day guide.
- `tv_archive / create_link` with `cmd="auto /media/<programmeId>.mpg"`. Programmes with `mark_archive=1` are playable. The client matches a requested start to the nearest archived start within 5 minutes.
- The client's design notes also list `itv / get_short_epg&ch_id=` (now and next), which the client does not call.

## 6. Movies and series

- `vod / get_categories` returns the same shape as genres.
- `vod / get_ordered_list` with `category, genre, sortby=added, fav=0, p`. Item fields: `id, name` (or `o_name`), `cmd, category_id, screenshot_uri, description, actors, director, genres_str, year, time` (minutes or `HH:MM:SS`), `rating_imdb` or `rating_kinopoisk, age, added, censored`.
- A movie whose file is asked for apart: `has_files` is set and `cmd` is `/media/<id>.mpg`. Then `vod / get_ordered_list&movie_id=<id>&p=1` gives the first file's `id`, and the command is `/media/file_<id>.mpg`.
- Series, three shapes:
  - **Inline:** `series:[1,2,...]` on the item and one `cmd`. Play with `create_link&series=<n>`.
  - **Seasons on request:** `is_series=1`. Then `get_ordered_list&movie_id=<id>&season_id=0&episode_id=0` lists seasons (`is_season`, `season_number`, `name`), and `season_id=<id>` lists episodes (`is_episode`, `series_number`, `name`, `time`, `screenshot_uri`, `description`).
  - **Separate `series` module:** `get_categories`, `get_ordered_list`, and `movie_id=<id>` gives seasons as `{id,name,cmd,series:[...]}`. A portal without it answers 404.
- `vod / create_link` with `cmd, series, ...`. The link may be an absolute URL or a path like `/media/...` relative to the portal.
- The client asks for the `series` module itself, and uses `vod` alone when the portal answers 404 (**Assumed**: from the client's author, not yet seen on the wire).
- Not said above, and open until a real client or portal shows it: the form of a movie's `cmd` when `has_files` is not set; whether `vod / create_link` puts a player prefix such as `ffmpeg ` before its link, as `itv / create_link` does (section 4); which module's `create_link` a client calls for an episode of the `series` module; the form of `added`; whether and how a client searches the lists; and whether a client tells the portal how far it got into a film, so as to resume it. Section 11 says what Overscan does about each.

## 7. Keep-alive

`watchdog / get_events` with `init=0, cur_play_type=1, event_active_id=0` answers `{"js":{"data":{"msgs":0}}}`. The client calls it at two thirds of the profile's `watchdog_timeout`, clamped to 30-120 s. This is also the natural channel for messages and events to the box.

## 8. What the client does not tell us

- The `/c/` web UI a real MAG loads, and any calls it makes that the client never needs (favorites, last-played, localization, modules, radio, and so on). The client impersonates a box from outside and only reads content.
- As far as is known, the MAG itself only needs to load our HTML page, so for MAG-native support we write both ends of those calls. The Ministra-style API matters for third-party Stalker clients, which must match what is above.
- Real hardware identity values (`sn`, `device_id`, `signature`) come from the box through the JS API in the page, not from cookies.

## 9. Target box: MAG322w1

- BCM75839, MIPS 4KE dual core 750 MHz, 512 MB RAM and flash, Linux 3.3, HEVC support, 100 Mbps Ethernet, Wi-Fi b/g/n 1x1 (150 Mbps), 2x USB 2.0, HDMI 1.4.
- Compatible with the MAG JS API (`gSTB`); Infomir's JS API docs are public.
- Firmware kept coming after 2.20.09 (2019, which added Opus and multi-portal fixes). **Observed:** a MAG322 runs 2.20.14, built in May 2025. Assume its browser stays as it is.
- **Observed** on a MAG322 with firmware 2.20.14, `navigator.userAgent` is `Mozilla/5.0 (Unknown; Linux) AppleWebKit/538.1 (KHTML, like Gecko) MAG200 stbapp ver: 4 rev: 734 Mobile Safari/538.1`, not the WebKit 533.3 string in section 2. The probe page that read it does not capture HTTP headers, so `X-User-Agent` from a real box is still unconfirmed. The browser runs ES5 and nothing newer; plan for simple CSS, a small DOM and a lean UI.
- Prefer Ethernet. Direct-play H.264/HEVC and avoid transcoding when the source allows it.

## 10. Seed for a conformance suite

The suite lives in [`conformance/`](../../conformance/README.md); its README lists the scenarios covered so far.

Port the fictional fixtures of the client's fake portal (three channels in two genres, one archived programme, three movies, one series module) and the scenarios in its test suite as black-box tests that run against our server: sign-in as a MAG, registered-device details, login portals, blocked and unknown boxes, paged channel lists, play-time links with token lapse and re-handshake, no-link reasons, the guide a channel at a time, past days, archive playback, watchdog timing, end-date formats, library paging, seasons and episodes, and movie/episode links. Fixtures are fictional; never put real portal addresses or credentials in the repo.

## 11. What Overscan answers so far

These are Overscan's choices where the sections above leave room. They are not observations of other portals.

- **Paths:** `/portal.php`, `/server/load.php` and `/stalker_portal/server/load.php`, by `GET` or `POST`.
- **Answers:** `{"js": ...}` as `application/json`, with numbers as strings in the profile.
- **Identity:** the `mac` cookie. A request without a valid one, or from a device that is unknown or waiting for approval, gets HTTP 401 with the body `Authorization failed.`. An unknown device is recorded as pending, up to 100 of them.
- **Handshake:** answers a new random token of 32 upper-case hex digits and ignores any token the client proposes. Only a hash of the token is stored, one per device, and a new handshake replaces it. Every other call needs it as `Authorization: Bearer <token>`.
- **Blocked devices:** can handshake and read a profile with `blocked` set to `"1"`. Every other call gets 401.
- **Devices whose account has ended** (D95): refused as blocked ones are, but `account_info / get_main_info` still answers, so that a client can show the end date, which the profile carries too.
- **`stb / get_profile`:** `id`, `blocked`, `watchdog_timeout` (`"120"`) and, if the account expires, `expire_billing_date` as `yyyy-MM-dd HH:mm:ss` in UTC. The hardware identity parameters (`sn`, `device_id`, `signature` and so on) are not checked yet. `stb_type`, or else the model in `X-User-Agent`, is recorded as the device's model.
- **`stb / do_auth`:** answers `true`. Accounts have no logins yet, so there is nothing to check.
- **`account_info / get_main_info`:** `end_date` in the same form when the account expires, otherwise an empty object.
- **`watchdog / get_events`:** `{"data":{"msgs":0}}`.
- **`itv / get_genres`:** `All` with ID `*` first, then the groups of the device's lineup in the order of their first channel. Groups with the same name from different sources are one genre.
- **`itv / get_all_channels` and `itv / get_ordered_list`:** the device's lineup by number, or by name with `sortby=name`, in pages of 14 for the ordered list (`genre` filters, `p` counts from 1). A channel's `id` is its lineup entry's, which stays the same across refreshes, and its `cmd` is `ffrt http://<host>/ch/<id>`, which names the channel for `create_link` and is not a stream address. `logo` is an address on Overscan, which serves the channel's logo from its own copy (D54), or empty for a channel without one. The archive flags `tv_archive`, `enable_tv_archive` and `archive` are `"1"`, and `tv_archive_duration` the archive's reach in hours, for a channel whose source keeps an archive that Overscan plays (D81); otherwise they are `"0"`. `fav=1` keeps the favourites of the profile the device plays as, which boxes on that profile mark (D53, D94). Once the account has a parental PIN, locked channels are left out, since a client cannot ask for the PIN (D55); a channel that its source marks for adults counts as locked, or is hidden, as the administrator chose (D86). A radio station (D87) is listed as a channel: the notes describe no `radio` module.
- **`itv / create_link`:** `{"cmd":"ffmpeg http://<host>/live/<token>.ts","error":""}`, with `.m3u8` for channels whose address ends in `.m3u8`. A `.ts` link always plays MPEG-TS: if the source answers with HLS, Overscan relays it as one continuous stream. The token is signed, bound to the device and the channel, and starts playback for a minute; the session it opens then carries reconnects and HLS segments. Overscan proxies the stream, so the device never sees its upstream address. `error` is `nothing_to_play` for a channel the device cannot play, a locked one among them, and `limit` when the channel's source has no free connection.
- **`itv / get_short_epg`:** the programme on now on the channel `ch_id` and the ones after it, `size` of them in all (10 if not given, at most 50), from the next 48 hours. A programme is `id`, `ch_id`, `name`, `descr`, `category`, `start_timestamp` and `stop_timestamp` (Unix seconds) and `mark_archive`, all strings. A programme's `id` is its channel's ID and its start, in whole minutes since 1970, as one number (the channel's ID times 2^26, plus the minutes), so that it stays the same when the guide is read again. `mark_archive` is `"1"` for a programme that has ended and plays from its channel's archive (D81). A channel that the device does not see, or that has no guide, gives an empty list.
- **`itv / get_epg_info`:** the programmes of every channel in the device's lineup over the next `period` hours (3 if not given, at most 24), keyed by channel ID. Channels without any are left out.
- **`epg / get_simple_data_table`:** the programmes on the channel `ch_id` that start on `date` (`YYYY-MM-DD`, today if not given) in the device's time zone, which its `timezone` cookie names, or else the server's. Pages hold 10, and `p` counts from 1; without it, the page is the one with the programme on now. The answer adds `cur_page`, and `selected_item`: the place on the page, from 1, of the programme on now, or 0.
- **`tv_archive / create_link`:** for a `cmd` whose last word is a path, or an address with a path, `/media/<id>.mpg`, with a programme's `id`: `{"cmd":"ffmpeg http://<host>/archive/<token>.ts","error":""}` (**Assumed**: the notes do not say what the answer is; it takes the form of `itv / create_link`'s). The link plays the programme from its channel's archive, from its start, through Overscan, which asks the source for it as the source says (D81, D82, D84; [catchup-notes.md](catchup-notes.md), and section 12 for a portal's); the token is signed, bound to the device, the channel and the programme, and starts playback for a minute. The programme is the channel's that starts within that minute. `error` is `nothing_to_play` for a programme that has not ended, began longer ago than the archive reaches back, or is on a channel that the device does not see or that has no archive that Overscan plays, a locked one among them once the account has a PIN (D55), and `limit` when the channel's source has no free connection. A client that seeks in it gets the programme from its start again: what a client sends to seek is not known.
- **`vod / get_categories` and `series / get_categories`:** `All` with ID `*` first, then the categories of films in `vod` and of series in `series` (D74), as the box's *Movies* and *Series* show them: categories of the same name from different sources are one. Without any source of films or series, the list is empty. A call when Overscan has not read any source's list yet reads them (D63) and waits up to 8 seconds; until a list has been read, the answer is `All` alone. Once the account has a parental PIN, locked shelves are left out, since a client cannot ask for the PIN (D85).
- **`vod / get_ordered_list` and `series / get_ordered_list`:** a page of 14 films or series (`p` counts from 1) in a `category` (`*` for all), the latest added first, or by name with `sortby=name`; `genre` is ignored. `fav=1` gives an empty list, since Overscan keeps no favourite films or series. `search` keeps those whose names hold its text, ignoring the case of letters A to Z (**Assumed**: the notes do not say how a client searches). An item has `id`, `name`, `cmd`, `category_id`, `screenshot_uri` (its poster, from Overscan's own copy), `description`, `actors`, `director`, `genres_str`, `year`, `time` (minutes), `rating_imdb` (out of 10, such as `7.4`), `age`, `added` (`yyyy-MM-dd HH:mm:ss` in UTC) and `censored` (`"0"`), all strings, with `""` for what is not known. What a title's source said when a device opened it comes before what its list said. A `cmd` is `/media/<id>.mpg`, which names the title for `create_link` and is not a stream address. `has_files` is never set, so `vod / get_ordered_list` with `movie_id` gives an empty list. A category that is an upstream portal's (D77) is that portal's pages: page `p` is the portal's page `p` of it, in the portal's order whatever `sortby` asks, with `total_items` the portal's count, and in `vod` without the series that a portal without a series module lists among its films, so such a page can hold fewer than 14; with `search`, a portal's category is searched as `*` is, for what has been read of it. Once the account has a parental PIN, locked titles are left out, and so is a locked series' `movie_id` list (D85); a page of a portal's category can then hold fewer than 14.
- **`series / get_ordered_list` with `movie_id`:** the series' seasons, 14 to a page, in the shape of the separate `series` module (section 6): `id`, `name` (its source's, or `Season N`, or `Specials` for season 0), `cmd` (`/media/<series id>_<season>.mpg`), `series` (the numbers of its episodes, as JSON numbers) and the series' own fields. Overscan reads the series' details and episodes from its source when a client asks for them, if it has none or they are old (D63), and waits up to 10 seconds; when the source is slower, the answer is what Overscan already has. An episode goes by its own number, unless its season has one without a number or two with the same, when the season's episodes go by their places in it, from 1.
- **`vod / create_link` and `series / create_link`:** for a film's `cmd`, or a season's with `series=<n>` (its first episode without it), `{"cmd":"http://<host>/vod/<token>.<ext>","error":""}`. The link has no player prefix (**Assumed**: a client that keeps a `cmd`'s last word reads it as it reads a channel's link, and one that takes the whole `cmd` can play it). It plays the file as it is, through Overscan, which passes on the player's range requests so that it can seek (D64, D75); the token is signed, bound to the device and the title, and starts playback for a minute. `error` is `nothing_to_play` for a title that the device does not see, one that is locked once the account has a PIN (D85), a series' own `cmd`, or an episode that the season lacks, and `limit` when the title's source has no free connection.
- **Anything else:** HTTP 404, and the module, action and parameter names go to the capture log with one example. Values of parameters that can carry credentials or identify the device are replaced with `REDACTED`, and other values are cut to 100 bytes.

## 12. What Overscan's client sends

Overscan also reads upstream portals, as the box that a provider registered (`internal/adapters/stalker`). Where the sections above leave room, these are its choices, all **Assumed** until a real portal shows otherwise. The fake portal its tests use (`internal/adapters/stalker/stalkertest`) is written from these notes alone.

- **Finding the API:** the addresses of section 1 in their order, keeping the first whose handshake answers JSON with a token. When one answers but refuses the box, the portal is said to refuse it rather than to be missing.
- **Handshake:** `token=` empty first; if the answer has no token, a random token of 32 upper-case hex digits with `prehash`, its SHA-1 in upper-case hex. The token kept is the answer's, or the one proposed when the answer has none.
- **Session:** one for each source, which its reads and its streams share, since a handshake replaces the box's token. Its calls run side by side, as a box's do; it signs in one call at a time.
- **Headers:** the `User-Agent` of section 2, unless the source names another, `X-User-Agent: Model: <model>; Link: WiFi` (the model is MAG250 unless the source names another), `Referer: <base>/c/`, and the cookie with the MAC address, `stb_lang=en` and the server's time zone, by its IANA name, or UTC where it has none.
- **Profile:** the parameters of section 3 with the serial number, `device_id`, `device_id2` and `signature` that the administrator gives, `hd=1`, `num_banks=2`, `client_type=STB`, `video_out=hdmi`, `auth_second_step=0`, `not_valid_token=0`, the current `timestamp`, and for `ver`, `image_version`, `hw_version` and `api_signature` the same fictional values as the conformance suite's client; `hw_version_2` and `prehash` are empty. A renewed `token` in the profile replaces the session's. A profile with `blocked` set stops the source.
- **Login:** `stb/do_auth` only when the administrator gives a login, after the profile. `false` stops the source.
- **A refused call** (401, 403, or a body that starts `Authorization failed.`) makes it hand-shake again, once, and try the call again; a second refusal stops it until the next call.
- **Channels:** `itv/get_all_channels`; where that answers 404, `itv/get_ordered_list` a page at a time, half a second apart. A logo that is a path is taken on the portal's host. A channel is for adults when its `censored` is set, or its genre's in `itv/get_genres` (D86). A portal's radio is not read: the notes describe no `radio` module (D87).
- **Links:** `itv/create_link` with `cmd`, an empty `series`, `forced_storage=undefined`, `disable_ad=0`, `download=0` and an empty `force_ch_link_check`; `vod/create_link` the same without the last, with `series` the episode's number for an inline series or a season of the series module, for films and episodes of either module. A film's or an episode's link is asked for as it starts, and again, once, when the portal answers its address with 401, 403, 404 or 410, as when a player seeks after the link has lapsed. The link is the last word of the answer's `cmd`, taken on the portal's host when it is a path; an `error` is the reason for none.
- **Links at play time:** a channel's link is asked for as its stream starts, and again when its player reconnects, since a link can lapse once played. When the source is at its limit of streams, the device's own stream from it ends before the next link is asked for, as a box stops one stream before it starts another. A `limit` error is the box's limit.
- **Streams:** fetched with the box's `User-Agent` (or the source's), without the cookie or the token.
- **Guide:** `itv/get_epg_info` with `period=30`, after a read of the channels when the last was 6 hours ago or more, read a programme at a time, each programme going to the channel stored under the portal's ID for it. A programme without a name, or whose stop is not after its start, is left out.
- **Past programmes:** a channel with `tv_archive`, `enable_tv_archive` or `archive` set keeps `tv_archive_duration` hours, rounded up to whole days. To play a past programme, the client finds it as section 5 says: it reads the channel's day guide, `epg/get_simple_data_table` with `ch_id`, `date` (`YYYY-MM-DD`, the day the programme began in the zone that its cookie names) and `p` from 1, a page at a time until a page reaches more than five minutes past the programme's start or the day ends, and takes the programme with `mark_archive` set whose start is nearest, within five minutes. Where the day has none, it reads the day before, or the day after for a programme that began after midday, since a portal may read `date` in its own zone. It then asks `tv_archive/create_link` with `cmd=auto /media/<id>.mpg` and the other parameters of `vod/create_link`, and takes the link as from `create_link`. **Assumed:** that a day guide's page has the form of a list's (section 1) and its programmes the fields of `get_epg_info`'s; that `date` is read in the cookie's zone; that `tv_archive/create_link` takes the other parameters and answers in the form of `itv/create_link`; and that the link plays the programme from its start, as a file that takes byte ranges. A programme's ID goes into the `cmd` only when it is letters, digits, `-`, `_`, `.` and `:`, at most 64 of them.
- **Past programmes at play time:** the link is asked for as the stream starts, and again, once, when the portal answers its address with 401, 403, 404 or 410, as a film's is. A link that is HLS (`.m3u8`) is relayed as one stream from the programme's start, and asked for again for each request after the first; any other is passed on with the player's ranges.
- **Films and series, as devices browse them:** the categories of `vod`, and of `series` where `get_categories` there does not answer 404, when a device first asks for films or series, and again a day later; a category's pages when devices ask for parts of its shelf, at most four for one request, each kept a day. A portal is not searched, and its lists are read only in the order `sortby=added`.
- **Films and series:** `get_categories` and `get_ordered_list` in `vod` and `series`, with `category`, `genre=*`, `sortby=added`, `fav=0` and `p`. A series module's seasons with `movie_id`; seasons on request with `movie_id`, `season_id=0` and `episode_id=0`, then a season's episodes with its `season_id`; a film with `has_files` by its first file, `/media/file_<id>.mpg`. A season's number is `season_number`, or the first number in its name, or its place. `time` is minutes, or `HH:MM:SS`; a rating is `rating_imdb`, or else `rating_kinopoisk`, out of 10. An item with `censored` set is for adults (D86).
- **Keep-alive:** `watchdog/get_events` while a stream plays, every two thirds of the profile's `watchdog_timeout`, between 30 and 120 seconds, and every 80 seconds when the profile gives none.
