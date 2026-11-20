# Device API

Overscan's own MAG page talks to the server through the device API, a JSON API under `/api/device/v1/` built for the box ([plan](../plan.md), section 9). The page and the server ship together, so the API changes with them. It is not meant for other clients: third-party players have the Stalker API.

Version 0, in M1, covers what the first page needs: a session, the channel list, and playing a channel. M2 adds what the page's new interface needs ([D52](../decisions.md)): the guide, favourites, what the box watched, search, and the box's own settings. M3 adds films and series ([D63](../decisions.md) to [D65](../decisions.md)), plays through ffmpeg those whose sound the box cannot play ([D68](../decisions.md)), checks what each box plays ([D70](../decisions.md)), and has Jellyfin transcode the video it cannot play ([D71](../decisions.md)). M4 adds past programmes from their sources' archives ([D81](../decisions.md), [D84](../decisions.md)), locks on films and series ([D85](../decisions.md)), radio stations ([D87](../decisions.md)), and recordings ([D89](../decisions.md)). M5 adds profiles ([D94](../decisions.md)), an account's end date ([D95](../decisions.md)), messages from the administrator ([D96](../decisions.md)), themes ([D97](../decisions.md)), recording a channel from now on ([D98](../decisions.md)), and each box's choice of how such a recording ends ([D99](../decisions.md)).

## Conventions

- Requests and answers are JSON in UTF-8. Numbers are JSON numbers, not strings.
- No answer may be cached: every one carries `Cache-Control: no-store`.
- A request body is at most 4 KiB, or 32 KiB for a playback check's answers, sent as `Content-Type: application/json`. A session call without that type is refused with HTTP 415: a page on another site cannot send it from a browser on the network without the server's leave, which it never gives.
- An error has an HTTP error status and a body of the form `{"error": "limit"}`:

| Code | Status | Meaning |
| --- | --- | --- |
| `bad_request` | 400, or 415 | The request could not be read, its body is not JSON, a value is not one the call takes, or a guide's window or list of channels is out of bounds. |
| `unauthorized` | 401 | There is no valid session: start a new one. |
| `blocked` | 403 | The administrator has blocked this box. |
| `expired` | 403 | The box's account has passed its end date ([D95](../decisions.md)): start a new session, which says so. |
| `not_found` | 404 | No such call, or no such channel, shelf, film, series or episode for this box. |
| `no_archive` | 404 | The programme does not play from its channel's archive: the channel's source keeps none that Overscan plays, the programme has not ended, or it began longer ago than the archive reaches back ([D81](../decisions.md)). |
| `recording_off` | 409 | The server does not record: the administrator has not turned recording on ([D89](../decisions.md)). |
| `programme_ended` | 409 | The programme has ended, so it cannot be recorded. |
| `too_many` | 409 | The household has 100 recordings to come or under way, the most it may. |
| `recorded` | 409 | A programme to come has been recorded already. |
| `pin_required` | 403 | The channel, film or series is locked: send the parental PIN ([D55](../decisions.md), [D85](../decisions.md)). |
| `pin_wrong` | 403 | The PIN is not the account's, or not the profile's. |
| `pin_locked` | 429 | Five wrong PINs in 15 minutes: `Retry-After` says how many seconds the box must wait before it tries again. |
| `limit` | 503 | The channel's, film's or episode's source has no free connection ([D36](../decisions.md)). |
| `busy` | 503 | The film's or episode's video must be transcoded, and the server transcodes as many titles as it may at once ([D73](../decisions.md)). |
| `source` | 502 | A Stalker portal could not be read for a part of its shelf, or gave no address for the film or episode ([D77](../decisions.md)). |
| `internal` | 500 | The server failed; its log says why. |

## Identity and pairing

A box is known by its MAC address and paired with a secret that it keeps, so that a copied MAC address alone is not enough ([D39](../decisions.md)). The page keeps the secret in a cookie, which the browser sends only with the page's own files, and in a file in the box's own storage, and either restores the other ([D38](../decisions.md)). It sends the MAC address and the secret to start a session:

- A box without the right secret asks to be paired. The answer gives it a new secret to keep and a six-digit code to show, and the box is recorded as pending. The administrator approves it with `overscan devices approve <code>`, or with its MAC address if the box has never been paired. While it waits, the box asks again every few seconds with its new secret, and gets the same code.
- Once approved, the box is paired with that secret, and its next session gets a token.
- A box that was approved by its MAC address before it first loaded the page, for instance before version 0.0.5, is paired at its first session, whose answer gives it a secret to keep.
- A paired box that has lost its secret, or another box with the same MAC address, asks to be paired again, and the paired box carries on meanwhile. Approving the new request by its code replaces the pairing and ends the old box's sessions.
- The Stalker API refuses the MAC address of a paired box: a Stalker client needs a MAC address of its own.

A session gives the box a token, which every other call sends as `Authorization: Bearer <token>`. A token lasts 24 hours, and ends sooner if the box is paired again. The server signs tokens with a key that it holds in memory, so a restart ends every session. On `unauthorized`, the page starts a new session and repeats the call.

## Calls

### `POST /api/device/v1/session`

Starts a session. The body names the box and gives its secret, if it has one:

```json
{"mac": "00:1A:79:12:34:56", "model": "MAG322", "secret": "<secret>"}
```

`model` and `secret` are optional; a secret has at most 100 characters. The page takes the MAC address and model from the box's JavaScript API (`gSTB.GetDeviceMacAddress` and `gSTB.GetDeviceModelExt`).

The answer says whether the box may use Overscan. For an approved box with its secret:

```json
{
  "state": "approved",
  "mac": "00:1A:79:12:34:56",
  "token": "<token>",
  "expires": 1794621600,
  "label": "Living room",
  "version": "v0.0.5"
}
```

`expires` is when the token stops working, in Unix seconds. `label` is the name that the administrator gave the box, if any, and `version` is the server's. A box paired at this session also gets `"secret"`, to keep. `ends`, when the box's account has an end date, is that end, in Unix seconds ([D95](../decisions.md)).

A box that asks to be paired, or waits for approval, gets `"state": "pending"` with the `code` to show, and a new `secret` to keep if it has just asked:

```json
{"state": "pending", "mac": "00:1A:79:12:34:56", "code": "481930", "secret": "<secret>", "version": "v0.0.5"}
```

A blocked box gets `"state": "blocked"`. An approved box whose account has passed its end date gets `"state": "expired"` with `ends`; the page says that the account has ended and asks again now and then, and the box's other calls answer `expired` until the account has a later end date, or none. None of these gets a token.

### `GET /api/device/v1/channels`

The whole lineup of the box's account, in one answer: the groups, in the order of their first channel, and the channels, by number. With them come the favourites of the profile the box plays as, in the order they were marked ([D94](../decisions.md)), and the channels this box played, the latest first, at most 30 ([D53](../decisions.md)). Both lists hold only channels of the lineup.

```json
{
  "version": "9f2c41d07ab3e815",
  "groups": [{"id": 3, "name": "News"}, {"id": 4, "name": "Sports"}, {"id": 5, "name": "Radio"}],
  "channels": [
    {"id": 17, "number": 1, "name": "Example News", "group": 3, "logo": "/artwork/logo/17/5f0c2a9d81b4e6c3f7a1d208", "archive": 7},
    {"id": 18, "number": 2, "name": "Example Sports", "group": 4, "locked": true},
    {"id": 19, "number": 3, "name": "Example Weather", "group": 0},
    {"id": 20, "number": 4, "name": "Example FM", "group": 5, "radio": true}
  ],
  "favourites": [18],
  "recent": [17, 18]
}
```

A channel's `id` belongs to its place in the lineup and stays the same when its source is read again. Its name, number and group are as the administrator has set them (D47). `group` is 0 for a channel in no group. `logo`, for a channel whose source gives one, is the path on Overscan of its logo, scaled to fit 240×96 ([D54](../decisions.md)); the path changes when the logo does, so the page may keep the image. When the logo cannot be served, the path answers 404 and the page shows the channel's name instead. `locked` is there, and true, when the channel needs the account's parental PIN to play: it, or its group, is locked, or its source marks it for adults and *Marked for adults* locks such channels, as it does at first ([D86](../decisions.md), [D91](../decisions.md)), and the account has a PIN ([D55](../decisions.md)). `archive`, for a channel whose source keeps an archive that Overscan plays, is how many days back its past programmes play from it ([D81](../decisions.md)). Groups are the lineup's, and the Stalker API's genres: a source's category goes to the group of its name, unless the administrator puts it in another, so channels from different sources whose categories have the same name share a group. A group's `id` stays the same when it is renamed. Hidden channels are left out, and so are those that their sources mark for adults when the administrator chose to hide such channels.

`radio` is there, and true, for a radio station: a stream that its source lists as one, which has sound and no picture ([D87](../decisions.md)). It plays as any channel does. `radio_apart` is there, and true, when the lineup has a radio station and the administrator chose to show radio stations on the box's Radio screen alone: the page then leaves them out of its lists of channels, its guide and its favourite channels, and a typed number finds them only on the Radio screen.

`version` changes when the groups or channels do, logos included, or `radio_apart`, and not when the favourites or the recent list do.

### `GET /api/device/v1/channels/version`

The lineup's `version`, `{"version": "9f2c41d07ab3e815"}`. The page asks for it every few minutes, and loads the channels again when it has changed, so the box shows the administrator's changes without *Restart Portal*. With it come the `messages` that the box has still to show, as with the channels.

### Messages

The administrator's messages to the box ([D96](../decisions.md)) come with `GET channels` and `GET channels/version`, the oldest first, while the box has some to show:

```json
"messages": [{"id": 12, "text": "The server restarts at 23:00.\nIt is back a minute later.", "sent": 1796414400}]
```

`text` is plain text of up to 500 characters, with line breaks: the page shows it as text, never as markup. `sent` is when it was sent, in Unix seconds. A message that the box has not shown within a week stops coming.

#### `POST /api/device/v1/messages/{id}/read`

Says that the box showed a message and the viewer closed it, and answers `{"id": 12, "read": true}`; the message stops coming. Saying so again changes nothing. A message that is not the box's answers `not_found`.

### `POST /api/device/v1/channels/{id}/play`

Answers how to play a channel through the stream hub. The body may be empty, or `{"pin": "1234"}`: a locked channel plays only with the account's parental PIN, and answers `pin_required` without it, `pin_wrong` with another, and `pin_locked` after five wrong ones in 15 minutes ([D55](../decisions.md)). A right PIN clears the count.

```json
{"url": "http://192.0.2.10:8080/live/<token>.ts", "solution": "ffrt"}
```

The page plays it with `gSTB.Play(solution + " " + url)`. The solution is the box's name for a kind of player: `ffrt` for MPEG-TS streamed live over HTTP, and `ffmpeg` for HLS. The link ends in `.m3u8`, with `ffmpeg`, only when the channel's address does; otherwise it ends in `.ts`, with `ffrt`, and if the source answers with HLS anyway, the hub relays it as one MPEG-TS stream ([D42](../decisions.md)). The link must be opened within a minute; the hub session that it starts then carries reconnects ([plan](../plan.md), section 7). Switching channels on a source that is at its connection limit works, since the box's own older stream makes room, but a stream from another device does not ([D36](../decisions.md)).

While the administrator has timeshift on, a channel whose link ends in `.ts`, other than a radio station, plays through a buffer on the server that keeps its last minutes ([D92](../decisions.md)), and the answer says how many seconds it keeps:

```json
{"url": "http://192.0.2.10:8080/live/<token>.ts", "solution": "ffrt", "timeshift": 1800}
```

The server reads the channel from its source into the buffer for as long as the session lasts, whether or not the box reads it, so a box that pauses its player picks up where it stopped. The link takes these parameters:

- `?behind=<seconds>` plays the channel from that long before live, from the last place a player can start from at or before then (in MPEG-TS, a packet of the stream's table), or from the oldest the buffer keeps. Without it, the link plays from live.
- `?hold=1` answers 204 and keeps the session, buffer and all, while the box is paused and its player reads nothing: the page asks every 15 seconds. A session that no request reads and no hold keeps ends 30 seconds after the last, as any other does, and its buffer with it. It answers 404 once the session has ended.
- `?stop=1` answers 204 and ends the session at once, with its buffer, so that the source's connection is free: the page asks when it leaves the channel.

A link without `timeshift` takes `hold` and `stop` too, and they do the same to its session.

### `POST /api/device/v1/pin`

Checks the account's parental PIN, `{"pin": "1234"}`, before the page shows something locked, such as a locked film's details ([D85](../decisions.md)). It answers `{"ok": true}` when the PIN is right, or when the account has none; otherwise `pin_required` for an empty one, `pin_wrong`, or `pin_locked`, with the wrong PINs counted as a play call counts them.

### `POST /api/device/v1/channels/{id}/archive`

Answers how to play a past programme of a channel from its source's archive ([D81](../decisions.md)). The body names the programme by when it began, in Unix seconds, as the guide gives it, with the parental PIN for a locked channel, as for `play`: `{"start": 1794528000, "pin": "1234"}`. The programme must have ended, and begun within the days of the channel's `archive`; otherwise the answer is `no_archive`.

```json
{"url": "http://192.0.2.10:8080/archive/<token>.ts", "solution": "ffrt", "seek": "restart",
 "start": "http://192.0.2.10:8080/archive/<token>/start", "duration": 3600,
 "programme": {"id": 4019, "title": "Evening News", "start": 1794528000, "stop": 1794531600}}
```

The link plays MPEG-TS without byte positions, and is played and sought in as a film's stream through ffmpeg: to play from a time, the page asks `start` with `?t=<seconds>` into the programme, which answers where the stream from that time starts, then plays `url` with `?t=` and that time, and counts the box's positions from it. An archive takes times to the minute, so a stream starts at the whole minute at or before the time asked for, which can be up to 59 seconds before the programme: `start` can answer less than 0. Each new request for the stream ends the one before, and the link's session goes on for six hours after the player stops, as a film's does. `duration` is the programme's length in seconds; the source's stream may run on past it. A source at its connection limit answers `limit`, as for channels.

A Stalker portal's channel plays a past programme at the link that the portal gives for it ([D84](../decisions.md)), which plays the programme from its start. Its answer has `"seek": "player"` and `"solution": "ffmpeg"`, as a film's that plays as it is, and no `start`: the box's player seeks in it itself, with byte ranges that Overscan passes on to the portal.

### `GET /api/device/v1/recordings`

Answers whether the server records, and the account's recordings that are to come or under way, the soonest first ([D89](../decisions.md)):

```json
{"on": true, "until": "programme", "max_hours": 4, "recordings": [
  {"id": 4, "channel": 17, "title": "Evening News", "start": 1796414400, "stop": 1796416200, "state": "scheduled"}
]}
```

`until` says how a recording that a box starts on a channel ends for a box that keeps the household's setting, `programme` or `stopped`, and `max_hours` the most hours that one which runs until it is stopped may run, as the admin's `recordings_until` and `recordings_max_hours` say ([D99](../decisions.md)).

`channel` is the lineup entry's `id`, as in the channel list, and `start` and `stop` are the programme's, in Unix seconds, as the guide gives them. `state` is `scheduled` until the recording begins, a minute before `start`, and `recording` until it ends, two minutes after `stop`. A channel recorded from now on has, as `start`, when it began, and as `stop`, the end of the programme it records; with `"until_stopped": true`, it runs until it is stopped, at the latest until `stop`, and ends there ([D98](../decisions.md)). Recordings that are done are films of the folder source that reads the recordings folder, on the box's Movies.

### `POST /api/device/v1/recordings`

Schedules a programme of a channel in the box's lineup, named by when it begins, as the guide gives it, with the parental PIN for a locked channel, as for `play`: `{"channel": 17, "start": 1796414400, "pin": "1234"}`. The programme must not have ended (`programme_ended`), the server must record (`recording_off`), and the household may have at most 100 recordings to come (`too_many`). The answer is the recording, as in the list. Scheduling a programme again answers the recording already scheduled, and a programme on now that the channel records already answers that recording; one that failed or was called off before it began is scheduled again, and one on now that was stopped while it recorded records what is left of it, from now. The server records it from a minute before its start to two minutes after its end, through the stream hub as a device of its own: it holds one of its source's connections all that time, and fails if the source has none free when it begins. It never ends a box's stream.

`{"channel": 17, "now": true, "pin": "1234"}` records the channel from now on, whether the guide shows a programme on it or not ([D98](../decisions.md)). While the guide shows one, the recording takes its title and stops at its end, two minutes after, unless the box records until stopped: as its `record` setting says, or, while that is `household`, as the household's `recordings_until` in the admin's settings does ([D99](../decisions.md)). Then, or without a programme, it runs until it is stopped, for at most the household's `recordings_max_hours`, and has `"until_stopped": true`. A channel that records already answers its recording. The server begins it at once.

### `DELETE /api/device/v1/recordings/{id}`

Calls off one of the account's recordings to come, which is `cancelled`, or stops one under way, which ends now and keeps what it has written, as a recording that is done. The answer is the recording.

### `PUT /api/device/v1/channels/{id}/favourite`, `DELETE` likewise

Marks a channel of the box's lineup as one of the favourites of the profile the box plays as, or unmarks it, and answers with which it now is: `{"id": 18, "favourite": true}`. Marking a favourite again, or unmarking a channel that is not one, changes nothing. A channel that the box does not see cannot be marked (`not_found`); one that has left the lineup can still be unmarked. Favourites are the profile's, so the other boxes and Stalker clients that play as it see the change ([D53](../decisions.md), [D94](../decisions.md)).

### `GET /api/device/v1/guide`

What is on every channel of the box's lineup, or on the channels that `ids` names, over a few hours, for now and next and for the guide's grid ([D51](../decisions.md)):

```json
{
  "from": 1794535200,
  "to": 1794546000,
  "channels": {
    "17": [
      {"id": 4021, "title": "Evening News", "start": 1794535200, "stop": 1794538800},
      {"id": 4022, "title": "Weather", "start": 1794538800, "stop": 1794540600}
    ]
  }
}
```

`from` and `to` are Unix seconds, in the query and in the answer. `from` is now if the query leaves it out, and `to` three hours after `from`; the window may last at most six hours. A programme is in it if it ends after `from` and starts before `to`. `ids`, if given, is a list of channel `id`s separated by commas, at most 100, such as the rows a screen shows: `GET guide?ids=17,18,19`. Channels are keyed by their `id`, and those without a programme in the window, or not in the lineup, are left out. The page shows times in the box's own time zone, and takes now from the answer's `from` when it leaves `from` out, so a box whose clock is wrong still shows what is on.

### `GET /api/device/v1/channels/{id}/guide`

One channel's programmes, with `subtitle`, `description` and `category` where its guide has them:

```json
{
  "from": 1794535200,
  "to": 1794621600,
  "programmes": [
    {"id": 4021, "title": "Evening News", "subtitle": "Headlines", "description": "The day's news.",
     "category": "News", "start": 1794535200, "stop": 1794538800}
  ]
}
```

The window is as for `GET guide`, but `to` is 24 hours after `from` if the query leaves it out, and the window may last at most 48 hours. A channel without a guide has an empty list; a channel that is not in the box's lineup is `not_found`.

### `GET /api/device/v1/search?q=`

What matches `q`, at most 64 characters, ignoring case: the channels of the box's lineup whose name holds it, or whose number it is, by number, at most 50; the programmes whose title holds it that are on now or start in the next 24 hours, the earliest first, at most 30; and the films and the series whose name holds it, by name, at most 30 of each, as a shelf lists them (below):

```json
{
  "channels": [17],
  "programmes": [{"channel": 18, "id": 5120, "title": "Evening Match", "start": 1794538800, "stop": 1794545400}],
  "movies": [{"id": 412, "name": "Evening Light", "year": 2024, "poster": "/artwork/poster/412/0c1d2e3f4a5b6c7d8e9f0a1b"}],
  "series": []
}
```

### Films and series

Overscan reads a source's films and series when a box first asks for them, and keeps them ([D63](../decisions.md)). A box browses them on shelves: what the household's account is part way through, what the sources added last, and the sources' categories, where categories of the same name from different sources are one shelf. A Stalker portal's categories are shelves of their own, whose titles Overscan reads a page of 14 at a time as boxes browse them ([D77](../decisions.md)). A film or a series keeps its `id` when its source is read again. Pictures are paths on Overscan, as logos are, which serve the source's picture scaled to the size that the page draws, and answer 404 when it cannot be served. A poster is 240×360; with `/artwork/thumb/` in place of `/artwork/poster/`, its path serves the same picture at 96×144, for rows of a list. Films and series come from enabled Xtream, Jellyfin and Plex servers, folders and Stalker portals.

#### `GET /api/device/v1/movies`, and `GET /api/device/v1/series`

Whether the box can browse the films, or the series, and their shelves:

```json
{
  "state": "ready",
  "shelves": [
    {"id": "continue", "name": "Continue Watching", "count": 2},
    {"id": "recent", "name": "Recently Added", "count": 100},
    {"id": "21", "name": "Drama", "count": 340}
  ]
}
```

| `state` | Meaning |
| --- | --- |
| `none` | No enabled source has films or series. |
| `loading` | Overscan is reading them for the first time, which the call waited a few seconds for: ask again in a moment. |
| `ready` | `shelves` lists them. A list that is a day old is read again meanwhile, and the box sees the new one when it asks next. |
| `failed` | The first read failed, and `error` says why, as its source answered. Overscan tries again when a box asks, ten minutes after the failure at the soonest. |

`continue` is there only when the account is part way through something, and `recent` only when a source says when it added its titles. A shelf without titles is left out. A portal's shelf is `count` `-1` until a part of it has been read, since only its pages say how many titles it holds.

#### `GET /api/device/v1/movies/shelves/{id}`, and `GET /api/device/v1/series/shelves/{id}`

A shelf's titles, `count` of them (200 if not given, at most 1000) from `from` (0 if not given):

```json
{
  "id": "21",
  "name": "Drama",
  "total": 340,
  "from": 0,
  "titles": [
    {"id": 412, "kind": "movie", "name": "Evening Light", "year": 2024, "rating": 74, "poster": "/artwork/poster/412/0c1d2e3f4a5b6c7d8e9f0a1b"},
    {"id": 418, "kind": "movie", "name": "Harbour Walls", "progress": 35},
    {"id": 431, "kind": "movie", "name": "Long Field", "watched": true}
  ],
  "next": 3
}
```

`next` is the `from` of the next part, `-1` after the last. A part of a Stalker portal's shelf ends where the pages that Overscan read for it end, at most four pages of 14, so it can hold fewer titles than `count`: ask for the next part from `next`. Its places are the portal's, and its titles come in the portal's order, whatever `order` asks. A portal without a series module lists its series among its films, so its shelf of films can hold series: `kind` says which a title is, and a series opens with `GET /api/device/v1/series/{id}`.

`locked` is there, and true, when playing the title needs the account's parental PIN: the administrator locked it or its shelf, or its source marks it for adults and *Marked for adults* locks such titles, as it does at first ([D85](../decisions.md), [D86](../decisions.md), [D91](../decisions.md)), and the account has a PIN. The page shows such a title without its picture until the PIN has been typed.

A category's titles come the latest added first, or by name with `order=name`. `recent` holds the last 100 that the sources added, the latest first, and `continue` what the account is part way through, the latest watched first. `rating` is out of 100. `year`, `rating` and `poster` are left out where the source gives none. `progress` is how much of a film the account has watched, in per cent, and `watched` is there for a film it finished. On `continue`, `episode` says which episode of a series plays next, such as `"S1 E3"`, and `progress` is that episode's: the one the account played last if it did not finish it, else the one after it.

#### `GET /api/device/v1/movies/{id}`, and `GET /api/device/v1/series/{id}`

A film or a series, with what its source says about it. The first time a box opens it, Overscan reads its details from the source, which the call waits for, up to ten seconds; it keeps them for a week, a series' for 12 hours, since series gain episodes.

```json
{
  "id": 412, "kind": "movie", "name": "Evening Light", "year": 2024, "rating": 74,
  "poster": "/artwork/poster/412/1d2e3f4a5b6c7d8e9f0a1b2c", "backdrop": "/artwork/backdrop/412/2e3f4a5b6c7d8e9f0a1b2c3d",
  "shelf": "Drama", "plot": "A long evening, and the light in it.", "genre": "Drama", "cast": "A. Actor, B. Actor",
  "director": "C. Director", "country": "Nowhere", "released": "2024-09-12", "age_rating": "PG", "runtime": 112,
  "details": true,
  "progress": {"position": 1260, "duration": 6730, "watched_at": 1794535200}
}
```

`details` is false while the source's details have not been read, and the title shows what the source's list said; `details_error` then says why they could not be read just now. `locked` is as on a shelf: the page checks the PIN with `POST /api/device/v1/pin` before it shows such a title. `runtime` is in minutes. `progress` is how far the account got into a film: `position` and `duration` in seconds, and `finished` once it played to the end, when `position` goes back to 0.

A series has, instead of `progress`, its seasons with their episodes, by number, and the episode to play next: the one the account played last, if it did not finish it, from where it stopped, or from the start if it stopped in the first 30 seconds; else the one after it; else the first.

```json
{
  "id": 77, "kind": "series", "name": "Coastline", "details": true,
  "seasons": [
    {"number": 1, "name": "Season 1", "episodes": [
      {"id": 901, "number": 1, "name": "Rocks", "duration": 2670, "picture": "/artwork/still/901/3f4a5b6c7d8e9f0a1b2c3d4e",
       "progress": {"position": 0, "duration": 2670, "finished": true, "watched_at": 1794531600}},
      {"id": 902, "number": 2, "name": "Sand", "duration": 2700}
    ]}
  ],
  "next": {"episode": 902, "position": 0}
}
```

#### `POST /api/device/v1/movies/{id}/play`, and `POST /api/device/v1/episodes/{id}/play`

Answers how to play a film or an episode through the stream hub ([D64](../decisions.md), [D68](../decisions.md)):

```json
{"url": "http://192.0.2.10:8080/vod/<token>.mkv", "solution": "ffmpeg", "seek": "player", "duration": 5412}
```

A locked film, or an episode of a locked series, plays only with the account's parental PIN in the body, `{"pin": "1234"}`, and answers `pin_required`, `pin_wrong` or `pin_locked` as a locked channel does ([D85](../decisions.md)).

The page plays it with `gSTB.Play(solution + " " + url)`, adding ` position:<seconds>` to start part way through. `duration` is in seconds, 0 if the server does not know it. With `seek` `player`, the link is the file as it is and ends in its extension; the session that it starts carries the player's range requests, so the player seeks in the file itself. The link must be opened within a minute; the session then goes on for six hours after the player stops, so a paused film goes on where it was. A source at its connection limit answers `limit`, as for channels.

A page that can play a stream that ffmpeg makes, seeking in it as below, says so with the body `{"restart": true}`. The server then reads what the file holds, the first time the film or the episode plays, which can take a few seconds. A file whose format, or whose sound track that plays first, the box cannot play comes through ffmpeg, which copies the video and converts that sound to Dolby Digital ([D68](../decisions.md)):

```json
{"url": "http://192.0.2.10:8080/vod/<token>.ts", "solution": "ffmpeg", "seek": "restart",
 "start": "http://192.0.2.10:8080/vod/<token>/start", "duration": 5412,
 "audio": [{"language": "eng", "title": "Surround", "codec": "dts", "channels": 6, "converted": true},
           {"language": "fre", "codec": "aac", "channels": 2}]}
```

The stream is MPEG-TS without byte positions, so the box cannot seek in it. To play from a time, the page first asks `start` with `?t=<seconds>`, which answers `{"t": 1234.567}`: where the stream from that time starts, at the picture at or before it. It then plays `url` with `?t=` and that time, without `position:`, and counts the box's positions from that time. `audio` lists the stream's sound tracks in the stream's order, the one that plays first ahead of the others: as the file tags them, with the file's codec and channels, and `converted` for one that the stream carries as Dolby Digital. Each new request for the stream, as each seek makes, ends the one before.

A film or an episode of a Jellyfin source whose video the box cannot play comes as a stream that the server transcodes ([D71](../decisions.md)), with `"audio_switch": "restart"`. It is played and sought in as above; `start` answers the very time asked for, since the server starts there. It carries one sound track at a time, the first of `audio` at first: the page picks another by playing `url` again with `?t=` and where it is, and `&a=` and the track's place in `audio`, counted from 0.

Where the administrator lets it, a film or an episode of another source whose video the box cannot play comes through this server's ffmpeg, which transcodes the video to H.264 ([D73](../decisions.md)). It is played and sought in as a stream through ffmpeg, with every sound track, each converted to Dolby Digital; `start` answers the very time asked for, since ffmpeg starts there. A box that asks while the server transcodes as many titles as it may at once for other boxes gets `busy`.

Either answer may list the subtitles that the page can load ([D69](../decisions.md)): text subtitles beside the file in a folder, or that a Jellyfin server has of it.

```json
"subtitles": [{"language": "eng", "url": "http://192.0.2.10:8080/vod/<token>/sub/0.srt"},
              {"language": "fre", "title": "Signs", "forced": true, "url": "http://192.0.2.10:8080/vod/<token>/sub/1.srt"}]
```

`language`, `title`, `forced`, `hearing` (for the deaf and hard of hearing) and `default` are there where known. `url` gives the track as SubRip in UTF-8, whatever its own format; for a stream through ffmpeg, the page adds `?t=` and the time the stream started at, and gets the subtitles moved earlier by as much. The session that the link starts serves them, so they stop working with it.

#### `PUT /api/device/v1/movies/{id}/progress`, and `PUT /api/device/v1/episodes/{id}/progress`

Keeps how far the profile the box plays as got into a film or an episode ([D65](../decisions.md), [D94](../decisions.md)). The page sends it now and then while one plays, and when it stops:

```json
{"position": 1260, "duration": 6730}
```

Both are in seconds; `duration` is 0 if the box does not know it. `{"finished": true}` says that it played to its end. Within the last twentieth of it, and at most the last ten minutes, it counts as finished too; before 30 seconds, as not started. The answer is what was kept, as `progress` above.

### `GET /api/device/v1/profiles`

The profiles of the box's account, the first first and then in the order they were added, and the one the box plays as ([D94](../decisions.md)):

```json
{"profile": 1, "profiles": [{"id": 1, "name": "Household"}, {"id": 4, "name": "Alex"}, {"id": 5, "name": "Sam", "pin": true}]}
```

A box plays as the profile that it or the administrator chose last, or as its account's first. The favourites and the resume points that the other calls give and keep are that profile's; the recent channels, the box's settings and its playback check stay the box's own, and the parental PIN and the locks the account's. `pin` is there, and true, when switching to the profile asks for its PIN. An account always has a profile, and the page shows profiles only when it has two or more. A profile's name is text that the page shows as it is, never as markup.

`theme` is there when the profile the box plays as has a theme other than Overscan's own: its own, or else its account's ([D97](../decisions.md)). It names the theme's stylesheet beside the page, such as `"theme": "theme-3.5d41c2a9b0.css"`, which the page loads after its own stylesheet; without it, the page takes that stylesheet away. The name changes when the theme or the page's stylesheet does, so the page asks for it again only then, and the server answers it without a session, as it does the page's own stylesheet: it holds the theme's colours, choices and brand, and nothing else of the account's. The page asks for the profiles when it starts and every five minutes, so a theme chosen in the admin shows within five minutes.

### `PUT /api/device/v1/profile`

Makes the box play as another of its account's profiles, with the profile's PIN if it has one: `{"id": 5, "pin": "1234"}`. It answers as `GET profiles` does, or `not_found` for a profile that is not one of the account's, and `pin_required`, `pin_wrong` or `pin_locked` as a locked channel does, with the wrong PINs counted with the parental PIN's. Switching to the profile that the box plays as asks for no PIN and changes nothing. The page then loads the lineup again, for the profile's favourites.

### `GET /api/device/v1/settings`, `PATCH` likewise

How Overscan's page looks and acts on this box ([D52](../decisions.md)). Each box has its own, kept by the server, so they survive a restart of the box:

```json
{"home": "simple", "picture": "dusk", "accent": "teal", "clock": "24", "numbers": "lineup", "groups": "preview", "record": "household"}
```

| Setting | Values | Default |
| --- | --- | --- |
| `home` | `simple`, six big buttons on a plain background; `picture`, a picture behind the home screen, the last channel and the favourites | `simple` |
| `picture` | the name of one of the page's own pictures: lower-case letters, digits and hyphens. A name the page does not know shows its first picture. | `dusk` |
| `accent` | `teal`, `white`, `blue`, `orange`: the colour of progress bars and the guide's now line | `teal` |
| `clock` | `24` or `12` hours | `24` |
| `numbers` | how the page numbers its lists ([D56](../decisions.md)): `lineup`, the lineup's numbers; `favourites`, the favourites from 1; `groups`, the favourites and each group from 1 | `lineup` |
| `groups` | what Live TV, Movies and Series do as the focus moves through their groups or shelves ([D62](../decisions.md)): `preview`, they show the channels or titles of the one that the focus rests on; `ok`, they show them only when OK or Right is pressed | `preview` |
| `record` | how a recording that the box starts on a channel ends ([D99](../decisions.md)): `household`, as the household's setting says; `programme`, at the end of the programme that the guide shows on the channel; `stopped`, when it is stopped, for at most the household's most hours | `household` |

`PATCH` changes the settings its body gives, keeps the others, and answers with them all. A value outside the table, or a setting not in it, is `bad_request`, and nothing changes.

### `GET /api/device/v1/check`, `PUT` likewise

The playback check ([D70](../decisions.md)) learns what the box plays. The page plays short clips, each in a format or with a codec that films have, and asks the viewer after each whether its picture moves or its tone sounds. ffmpeg makes the clips on the server the first time a box asks, which takes about a minute; the server then keeps them in `check` under its data directory. `GET` says where that stands:

```json
{"state": "ready", "ready": 23, "total": 23, "checked": 1795424400,
 "clips": [{"id": "base", "label": "MKV with H.264 and AAC", "ask": "both", "url": "http://192.0.2.10:8080/api/device/v1/check/<token>/base.mkv",
            "solution": "ffmpeg", "width": 640, "height": 360},
           {"id": "dts", "label": "DTS", "ask": "sound", "url": "http://192.0.2.10:8080/api/device/v1/check/<token>/dts.mkv",
            "solution": "ffmpeg", "width": 640, "height": 360}]}
```

`state` is `preparing` while the clips are made, with `ready` of `total` done, and `unavailable` when the server cannot make them: without ffmpeg, or with an ffmpeg that cannot make the first clip. A clip whose encoder ffmpeg lacks is left out. `checked` is when the box was last checked, in Unix seconds, or 0. `clips`, once `ready`, are in the order to play them. The page plays each with `gSTB.Play(solution + " " + url)`; the link needs no session, and lasts two hours. `ask` says what to ask once it plays: `picture`, whether the picture moves; `sound`, whether the tone sounds; or `both`. The first clip holds what every box plays, so a box that does not play it cannot be checked.

`PUT` sends what the page found of each clip it played:

```json
{"firmware": "2.20.16-322",
 "results": [{"id": "base", "answer": "yes", "started": true, "failed": false, "ms": 640, "width": 640, "height": 360, "tracks": 1, "position": 3.4,
              "video": "{frameRate:25000,pictureWidth:640,pictureHeight:360}", "audio": "[{pid:257,lang:[\"\"],type:4}]"},
             {"id": "dts", "answer": "no", "started": true, "failed": false, "ms": 700, "width": 640, "height": 360, "tracks": 1, "position": 4.1,
              "video": "", "audio": ""}]}
```

`answer` is the viewer's, `yes` or `no`, or `""` when they were not asked or did not know. `started` and `failed` say whether the clip started (the box said so, its place in the clip moved, or the viewer said it plays), or the box could not open it; `ms`, how long it took to start; `width` and `height`, the picture's size as the box reported it, 0 if it did not; `tracks`, how many sound tracks it listed; `position`, where it had got to, in seconds, when the viewer answered; `video` and `audio`, what the box's player said of the picture and the sound, as it said it, at most 300 bytes each; and `events`, if any, the player's events while the clip played, as the box gave them (`4 2 7`, or `"4" "2" "7"` when it gives them as text), at most 200 bytes. The support bundle shows them. A clip answered `no`, or that the box did not start, does not play. The server keeps what the check found, in place of what an earlier check found, and answers with it, in the check's order:

```json
{"checked": 1795424400, "findings": [{"label": "MKV", "plays": true}, {"label": "DTS", "plays": false}]}
```

Films and episodes then play on the box by what it found: its model's profile, with what plays added and what does not taken away. A box never checked plays by what the latest check of a box of its model found. A check whose first clip did not play, a clip twice or one the check does not have, or a value out of bounds, is `bad_request`, and nothing is kept.

### `POST /api/device/v1/playback`

Tells the server how a film or an episode started on the box, or did not, for its log. It changes nothing; a box test reads the log to see why a film stood still. The page sends it once the box shows a film moving, and when one stands still for a while:

```json
{"kind": "movie", "id": 42, "outcome": "began", "link_ms": 2300, "moved_ms": 5400, "event_ms": -1, "wait_ms": 0,
 "nudged": true, "converted": false, "from": 0, "events": "\"2\" \"7\" \"4\""}
```

`kind` is `movie`, `episode`, or `programme` for a past programme ([D81](../decisions.md)), and `id` the film's, the episode's, or the channel's. `outcome` is `began`; `not_started`, when the box showed nothing moving for 40 seconds, and the player never said it started; `waited`, when it stood still for more than 10 seconds while it played, then went on; or `stalled`, when it stood still for 40 seconds and did not. The page starts a film that did not start, or stalled, again. Times are in milliseconds: `link_ms`, how long the server took to give the link; `moved_ms`, from the play command until the box's place in it moved, `-1` if it never did; `event_ms`, until the player said it started, `-1` if it never did; and `wait_ms`, how long it stood still. `nudged` says that the page paused it and played it again, six seconds after it was given to the box with nothing moving, as a viewer might. `converted` says it came through ffmpeg; `from`, where it started, in seconds; `events`, the player's events since the play command, as the box gave them, at most 200 bytes. The answer is `{}`, or `not_found` for a title that has gone; a box that sends more than 30 in a minute gets `busy`.
