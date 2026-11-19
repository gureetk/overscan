# Folders of films and series

How Overscan reads a folder of films and series on its server: which files it lists, how it tells films from episodes, and what it takes from the `.nfo` files and pictures beside them. It follows the layouts that Kodi, Jellyfin and Plex read, so a folder laid out for one of them needs no changes. Overscan only reads the folder; it never writes to it.

The code is `internal/adapters/folder`; D67 is the decision.

## Adding a folder

In the admin, *Sources* → *Add a source* → *Folder*: the folder's absolute path on the server, such as `/srv/media/films`. *Test* says how many films and series Overscan finds there. Each folder is one shelf on the box's *Movies* and *Series*, named after the folder, so several folders make several shelves. Overscan reads a folder when a box opens *Movies* or *Series*, and again once a quarter of an hour has passed, so a film copied into it shows up soon after; a film, or a series with its episodes, is read again when it is opened. On its schedule, Overscan also checks that the folder can still be read. In the configuration file, a folder is `type: folder`, with its path as `url`.

The service has to be able to read the folder:

- Overscan runs as the `overscan` user, which needs to read the folder, the folders in it and their files.
- The service's sandbox (`ProtectHome=yes` in its unit) hides `/home`, `/root` and `/run/user` from it. Keep the films elsewhere, such as under `/srv` or `/mnt`, or give the unit a drop-in that sets `ProtectHome=read-only`.
- In a Proxmox container, mount the folder into the container; in Docker, mount it as a volume, as the comment in `compose.yaml` shows, and name the path inside the container.

Films and episodes play from the files as they are, through Overscan, which answers the box's requests for parts of a file so that it can seek.

## What it lists

- Videos with one of these extensions, in any case: `mkv`, `mp4`, `m4v`, `avi`, `mov`, `wmv`, `mpg`, `mpeg`, `ts`, `m2ts`, `mts`, `webm`, `flv`, `ogv`, `3gp`, `divx`.
- It leaves out:
  - folders whose names start with `.`, `@`, `#` or `$` (hidden folders, and those that file servers keep, such as `@eaDir`);
  - a title's extras: the folders `Extras`, `Featurettes`, `Behind The Scenes`, `Deleted Scenes`, `Interviews`, `Scenes`, `Samples`, `Shorts`, `Clips`, `Trailers`, `Other` and `Backdrops`, and videos whose names end in `-trailer`, `-sample`, `-featurette`, `-behindthescenes`, `-deleted`, `-interview`, `-scene`, `-clip`, `-short`, `-other` or `-extra`;
  - discs' folders (`VIDEO_TS`, `BDMV`);
  - what lies more than 12 folders deep.
- A link (symbolic link) is followed only while it stays inside the folder. One that leads outside is left out, and so is anything a link to a folder leads to.

## Films

A video that is not an episode is a film. Its name and year come from:

1. its `.nfo` file, if it has one that gives them;
2. else its own folder's name, when the film has a folder of its own: the folder holds no other film, and its name has a year in brackets, or is the film's name, or the video is called `movie`, `film`, `video`, `main` or `feature`. So `The Long Field (2024)/the.long.field.2024.1080p.mkv` is *The Long Field*, 2024;
3. else the video's own name.

From a name, Overscan takes the title up to a year in brackets, as in `Title (2024)`, and leaves out tags in square or curly brackets, such as `[1080p]` or `{tmdb-123}`. In a release's name, with dots or underscores for spaces, it stops at the first word that says how the release was made (`1080p`, `BluRay`, `x264` and the like) and takes the last year after the first word as the year: `Blade.Runner.2049.2017.1080p` is *Blade Runner 2049*, 2017. A number that is still to come is never a year, and in a name with spaces a year without brackets stays in the title.

## Series

A video is an episode when its name gives a season and an episode, as `S01E02`, `s1e2`, `S01.E02` or `1x02`, or when it sits in a season's folder (`Season 1`, `Season 01`, `S01`, and the like in several languages) and its name gives the episode, as `E02`, `Episode 2` or a number that starts it (`02 - Name.mkv`). `Specials` is season 0. The words after the episode's number name it, unless they say how the release was made.

The series is:

- the folder that holds the season's folder: `Coastline (2019)/Season 1/Coastline S01E02.mkv`;
- else the folder that holds the episode, when the episode's name gives no series, or one that the folder's name starts with: `Coastline/Coastline S01E02.mkv`;
- the whole folder, when the folder is a series' own: its seasons' folders are at its top, or episodes whose names give no series, or the one that the folder's name starts with;
- else the series that the episode's name gives: `Downloads/Night.Shift.S01E02.mkv` belongs to *Night Shift*, with the other episodes of that name, wherever they are.

A series' name and year come from its `tvshow.nfo`, else from its folder's name, as for films.

## .nfo files

Overscan reads the `.nfo` files that Kodi writes, which Jellyfin and Plex's agents read too:

- a film's: the video's name with `.nfo` (`Title (2024).nfo`), or `movie.nfo` in the film's own folder;
- a series': `tvshow.nfo` in its folder;
- an episode's: the video's name with `.nfo`. A file for several episodes describes the first.

It takes the title, the year, the plot (or else the outline), the genres, the directors, the first eight actors, the countries, the age rating (`mpaa`, as in `Rated PG-13` or `US:PG-13`), the rating (the default one of `ratings`, else `rating`, out of 10 unless it says otherwise), the running time in minutes, the date it was first shown (`premiered`, `releasedate` or `aired`), an episode's season and number, and the title's ID on TMDB (`uniqueid` of type `tmdb`, or `tmdbid`). A file in UTF-8 or ISO-8859-1 is read; one that holds only an address, as some do, is not.

## Pictures

JPEG or PNG files, up to 16 MiB, with names in any case:

| Picture | Names |
| --- | --- |
| A film's poster | the video's name with `-poster` or nothing more (`Title (2024)-poster.jpg`, `Title (2024).jpg`); in the film's own folder, `poster`, `folder`, `cover`, `default` or `movie` |
| A film's wide picture | the video's name with `-fanart` or `-backdrop`; in its own folder, `fanart`, `backdrop`, `background` or `art` |
| A series' poster | `poster`, `folder`, `cover`, `default` or `show` in its folder |
| A series' wide picture | `fanart`, `backdrop`, `background` or `art` in its folder |
| An episode's picture | the video's name with `-thumb` or nothing more |

The box gets them through Overscan, scaled to the size it draws. A picture that changes gets a new address, so boxes fetch it again.

## Details from TMDB

With a key of your own from The Movie Database (TMDB), given on the admin's *Settings* page, a film or a series that lacks a poster or a plot gets what its folder leaves out from TMDB: the poster, the wide picture, the plot, the genres, the cast, the director, the running time, the date and the rating, and for a series its seasons' names and pictures, and its episodes' names, plots and pictures (D88). What the folder has always comes first. Overscan finds a title on TMDB by the ID in its `.nfo` file, or in a tag of its name or its folder's name (`[tmdbid-123]` or `{tmdb-123}`), and otherwise by its name and year, taking only a title of the same name within a year of it. It looks titles up in the background after a folder is read, one request at a time, and asks again after 30 days, or after 7 for a title that TMDB had no match for. A series' episodes are read from TMDB when a box opens it. Without the key, nothing is asked of TMDB, and what it said is forgotten. [protocol/tmdb-notes.md](protocol/tmdb-notes.md) says what is asked of TMDB.

## Recordings

The programmes that boxes record (D89) go in a folder that a folder source named `recordings` reads, which Overscan adds when recording is turned on. A recording's file is named `<title> - <channel> - <date> <time> [<number>].ts`: the folder source takes the title up to the number in square brackets, or up to a year in brackets in the programme's title, and a programme whose title has a season and an episode, such as `S01E02`, shows as an episode on *Series*. While it records, the file's name starts with a dot, which the folder source passes over, so a recording shows on *Movies* once it has ended.

## Subtitles

Text subtitles beside a film or an episode show on the box (D69): SubRip (`.srt`), WebVTT (`.vtt`) and SubStation Alpha (`.ass`, `.ssa`). A file belongs to a video when its name starts with the video's name and a full stop, such as `Title (2024).en.srt`, in the video's folder or in a `Subs`, `Subtitles` or `Sub` folder beside it. In such a folder, when the video is the only one in its own folder, any subtitle file belongs to it, such as `Subs/2_English.srt`. What the name says after the video's name, in any order:

- a language, as a two-letter or three-letter code or its English name (`en`, `eng`, `English`);
- `forced`, for subtitles that show only what is not in the film's language, such as signs;
- `sdh`, `cc`, or `hi` after a language, for the deaf and hard of hearing;
- `default`;
- anything else, as the track's title, such as `Commentary`.

On the box, Blue while a film plays lists them, with the ones in the file itself when the box plays the file as it is. A file in UTF-8 or UTF-16 is read as it is; one in an older code page is read in the one its language used (Windows-1251 for Russian, Windows-1250 for Polish and so on), and otherwise Windows-1252. Subtitles made of pictures (`.sub` and `.idx`, `.sup`) are not shown.
