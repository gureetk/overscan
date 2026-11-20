# The MAG page

Overscan's own interface for MAG set-top boxes. Overscan serves it at `/c/`, and at `/stalker_portal/c/` for a box that is set up for a Stalker portal; the box loads it as its portal. It talks to the server through the [device API](../../docs/api/device.md).

It pairs the box once the administrator approves it, then shows Overscan's own theme ([D52](../../docs/decisions.md)):

- **Home**, *Simple* (six big buttons) or *With a Picture* (a picture, the last channel watched and the favourite channels), as the box's settings choose.
- **Live TV**: the groups, a group's channels with what is on now, and a small player with the programme of the channel in focus. Moving through the groups shows a group's channels once the focus rests, first as text, unless the box opens groups with OK only. OK plays the channel in the small player; OK again goes full screen.
- **Watching**: full screen, with an info banner for the channel, what is on now and next, and the picture and sound.
- **Movies** and **Series**: shelves of what the account is part way through, what the sources added last, and the sources' categories; a title's details, with Play or Resume; a series' seasons and episodes. A film or an episode plays full screen, with a panel for where it is, and the next episode follows the one that ends. Overscan reads a source's films and series when the box first asks for them, so the first visit may wait a moment ([D63](../../docs/decisions.md)).
- **The guide**: two hours of a group's channels at a time, a week ahead.
- **Search**: channels by name or number, programmes on now or in the next day, and films and series by name.
- **Recordings**, from Home while the server records: the household's recordings under way, those to come, and those of the last day that are done or failed. Green stops one under way, keeping what it recorded, or cancels one to come; OK plays its channel, or opens Movies for one that is done ([D100](../../docs/decisions.md)).
- **Settings**: this box, Appearance (Home's look and picture, the accent colour, the clock), the channel numbers (the lineup's, or from 1 in the favourites or in each group, [D56](../../docs/decisions.md)), whether Live TV's groups, and the shelves of Movies and Series, show what they hold as the focus moves or only on OK ([D62](../../docs/decisions.md)), how a recording started on a channel stops on this box while the server records ([D99](../../docs/decisions.md)), the parental controls, reloading the page, and what it is.

Locked channels ask for the parental PIN ([D55](../../docs/decisions.md)). How far the account got into a film or an episode is kept on the server, for all its boxes ([D65](../../docs/decisions.md)), each time it pauses or stops and every half minute while it plays. The page asks the server every few minutes whether the lineup changed, and loads it again if it did. [Trying Overscan](../../docs/trying-it.md) says how to put the page on a box.

## Keys

| Remote | What it does | Keyboard, in dev mode |
| --- | --- | --- |
| Arrows, OK | Move, choose. Playing a film, or watching a channel paused or behind live: Left and Right go back 10 seconds and on 30; OK pauses and plays a film | Arrows, Enter |
| Back | Go back; in search, delete a letter | Backspace |
| Exit, Menu | Go to Home | Esc; Home for Menu |
| Channel +/−, Page up/down | Change channel while watching; page through a list. Playing a film: on or back 5 minutes | Tab and Shift+Tab, Page Up and Page Down |
| 0–9 | Go to a channel by its number | Digits |
| Red | Add the channel to the favourites, or remove it | F1 |
| Green | Guide: record a programme, or stop or call off its recording. Live TV, and watching: record the channel from now on, or stop its recording. Watching, while the server keeps live TV: pause, and play on instead | F2 |
| Yellow | Live TV: sort by name or number. Movies and Series: sort a shelf by name or by date added. Watching, or playing a film: the sound track. Guide: the group | F3 |
| Blue | Search; while watching, the guide | F4 |
| Info | Show or hide the banner while watching, or the panel while a film plays | Alt+Y |
| Guide, TV | The guide; Live TV, or the channel that plays | F8, F10 |
| Volume, Mute | Volume, mute | + and − on the number pad, Alt+` |
| Stop | Stop playing; a film stops and the page goes back to where it was chosen. Watching a channel paused or behind live: back to live | Alt+S |
| Play/Pause, Rewind, Fast-forward | Watching, while the server keeps live TV: pause and play on; back and on a minute | Pause or Alt+R; Alt+B, Alt+F |
| REC | Guide: as Green. Live TV, and watching: record the channel from now on, or stop its recording | Alt+W |

## Building

The page is written in TypeScript and built into `dist/`, which is committed: the server embeds it, so building Overscan needs no Node.js. Building the page needs Node.js 22 or later.

    npm ci --ignore-scripts
    npm run check

`check` type-checks the sources, builds `dist/` and lints the build. After a change, commit `dist/` with the sources; CI builds the page again and fails if the two differ.

The toolchain follows from what the MAG322w1's browser supports ([D32](../../docs/decisions.md)):

- TypeScript checks types against its `es5` and `dom` libraries, so built-ins that the box lacks, such as `Promise` and `Map`, are type errors. It emits nothing.
- Babel compiles each file to ES5, Rollup bundles them into one script, and terser minifies it for ES5. The build parses the result as ES5 and fails on anything newer.
- ESLint checks the built script for what the `dom` library declares but the box lacks: `fetch`, `URLSearchParams`, a working `URL`, `TextDecoder`, `Intl`, `localStorage` and XHR's `json` response type.
- Autoprefixer adds the `-webkit-` prefixes that the box needs. The styles use no grid and no `gap`, which the box's browser lacks.
- No polyfills and no framework.

Every file that the build writes, but `index.html`, carries a hash of its content in its name, and the server never lets a box cache `index.html`, so a box always runs the page that the server ships.

## Fonts and pictures

`src/assets/` holds files that the build copies into `dist/` as they are:

- `fonts/`: Instrument Sans, by the Instrument Sans Project Authors, under the SIL Open Font License 1.1 (`fonts/OFL.txt`, which the build copies too). `tools/fonts.py` made these files from [the upstream fonts](https://github.com/Instrument/instrument-sans): the four weights the page uses, cut down to Latin text, with tabular figures as the default figures, as WOFF.
- `pictures/`: the pictures for Home *With a Picture*. `tools/pictures.py` draws them from gradients and noise, so they are Overscan's own; the same script always draws the same pictures.
- `mark.svg`: Overscan's mark, from `docs/logo`.

The stylesheet names an asset as `url("asset:fonts/x.woff")`, and the script imports the names from the module `assets`.

## Trying it in a desktop browser

Run Overscan and open `http://localhost:8080/c/?dev`. In dev mode the browser stands in for a box with the MAC address `02:00:00:00:00:01`, or another one given as `?dev&mac=...`, which is paired like any other box with `overscan devices approve`. A panel stands in for the video, full screen or in the small player, and says what the box would play. The keyboard sends what the remote sends, as the table above says. `?dev&fail=dts,av1` has the addresses of the playback check's clips that hold those names fail to play, as a box that cannot open them would.

## The sources

| File | What it does |
| --- | --- |
| `src/main.ts` | Starts the page on the box or in dev mode, and sizes it to the screen |
| `src/app.ts` | Pairing, loading the lineup and the settings, moving between screens, the keys, and where the video shows |
| `src/screens/` | The screens: Home, Live TV, watching, the guide, Movies and Series, a title's details, a series' episodes, playing a film, search, settings, the playback check, and messages |
| `src/overlays.ts` | What shows over any screen: a short message, the number being typed, the PIN, a list to pick from |
| `src/player.ts` | Playing a channel: the link, the PIN, starting a stream again, changing channel |
| `src/film.ts` | Playing a film or an episode: where it starts, seeking, pausing, starting it again where it broke off, and keeping how far it got |
| `src/library.ts` | The films and series: their shelves, a page at a time, and a title's details |
| `src/check.ts` | The playback check ([D70](../../docs/decisions.md)): playing the server's clips one after the other, what the viewer answers, and sending what it found |
| `src/lineup.ts`, `src/guide.ts`, `src/settings.ts` | The lineup with the favourites and the recent list; what is on; the box's settings |
| `src/api.ts` | The device API, over `XMLHttpRequest` |
| `src/time.ts` | The server's time, and how times and dates are written |
| `src/secret.ts` | The device secret that pairs the box, kept in a cookie and in a file in the box's storage |
| `src/list.ts` | A list that draws only its visible rows |
| `src/keys.ts` | The remote's keys |
| `src/dom.ts`, `src/icons.ts` | Small helpers for elements, logos and icons |
| `src/platform.ts`, `src/mag.ts`, `src/dev.ts` | The box's player, video window, sound tracks and volume, through its JavaScript API, and dev mode's stand-in |
| `src/stb.d.ts` | Types for the parts of the box's JavaScript API that the page uses |
| `src/style.css` | The styles, in `rem` on a 1280×720 grid ([D33](../../docs/decisions.md)) |
