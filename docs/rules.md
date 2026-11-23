# A lineup's rules

A rule changes many channels of a lineup at once: it shows, hides, renames, moves, numbers or gives a logo to the channels that it matches, gives them another channel's guide, moves their guide's times, or merges the copies of a channel into one. Rules run again after every refresh of a source, so they keep up with the channels that a provider adds, renames or moves ([D107](decisions.md)). The administrator keeps them on the *Live TV* page's *Rules* tab, for the lineup that the page shows; the admin API's `lineup/rules` paths do the same ([api/admin.yaml](api/admin.yaml)).

Rules sit beside the edits made by hand on the *Channels* tab ([D47](decisions.md)), and never change them: a name, a group, a visibility or a number set on a channel by hand wins over every rule. The *Channels* tab marks a channel that a rule changes with *Rule*, and says what the rules do to it. In a lineup of your own groups, rules act only on the channels that its groups take: a rule cannot bring in a channel whose category no group takes ([D136](decisions.md)).

## What a rule matches

A rule acts on the channels that match everything it gives, and on every channel if it gives nothing:

| Condition | What it looks at |
|---|---|
| Source | The source that lists the channel. Removing the source removes the rule. |
| Category | The channel's category, as its source names it. |
| Name | The channel's name, as its source gives it, before any rule or edit renames it. |
| Guide ID | The channel's guide ID (its `tvg-id`, or what its source gives as one). |
| Quality | A word that the channel's name holds: SD, HD, FHD, UHD, 4K, 8K, HEVC, H264, H265, 1080p, 720p, 50fps or 60fps. Words are runs of letters and digits, so *HD* does not match *FHD*. |

Category, name and guide ID take a pattern. A pattern matches the whole text, whatever its case: `*` stands for any characters and `?` for one. Between slashes, a pattern is a regular expression in [Go's syntax](https://pkg.go.dev/regexp/syntax), which matches anywhere in the text, whatever its case.

| Pattern | Matches |
|---|---|
| `Sport*` | Sport, Sports, Sport Extra |
| `*news*` | News 24, World News, Local news HD |
| `Channel ?` | Channel 1, Channel A |
| `/\+1$/` | Names that end in +1 |
| `/^(Granite\|Harbour) /` | Granite News, Harbour Sport |
| `/backup\|test/` | Anything with *backup* or *test* in it |

Since rules match what the source says, one rule's renaming or regrouping never changes what another matches.

## What a rule does

| Action | On the page | What it does |
|---|---|---|
| `include` | Show them | Shows the channels, even in a hidden group. |
| `exclude` | Hide them | Hides them. |
| `rename` | Rename them | Gives them a new name, or replaces text in their names, as the rules before it left them. The text to replace is matched whatever its case; between slashes it is a regular expression, and `$1` in what replaces it is its first group. A name that would be empty stays as it was. |
| `regroup` | Move them to a group | Moves them to the lineup's group of that name, whatever its case, or else to a new group, which goes again once nothing is in it. |
| `renumber` | Number them | Numbers them in a row from a first number, in their order. The channels that it numbered keep their numbers as others come, after them. Numbers pinned by hand are skipped, and channels that held the others get new numbers after the highest, as new channels do. |
| `logo` | Give them a logo | Gives them the logo at an http or https address, which devices then load: it must be one that they can reach. |
| `guide` | Use another channel's guide | Shows on them the guide of the lineup's channel with the name it gives (`channel`), or with the name that replacing text in their own makes (`replace` and `with`, as for `rename`), whatever its case. The other channel is one of the same source if there is one; it shows its own guide, never what a rule gives it ([D108](decisions.md)). A channel that none is named as keeps its own guide. |
| `timeshift` | Move their guide's times | Shows their guide's programmes a number of minutes later, or earlier, up to a day either way (`minutes`, below 0 for earlier). |
| `merge` | Merge copies into one channel | Finds the copies of a channel among them, which a source lists more than once, in another quality or as a second feed: those with the same guide ID (`copies: guide`), or the same name once the words of the quality list, case, marks and spaces are left out (`copies: name`), after replacing text as `rename` does if it gives some. Of each set of copies, the first that devices see shows: the first from the sources that it puts in order (`sources`, by ID in the admin API and by name in a lineup file, [D137](decisions.md)) if it names some, then the first in a quality (`prefer`) if it gives one, else the first in the lineup. A source removed leaves the order, and the rule stays. The others are hidden, and play in its place, in turn, when its stream cannot start, after the backups picked by hand, five at most ([D113](decisions.md)). A copy that its source no longer lists, or whose source is off, is no one's. |

## Order

Rules run from the first to the last. Where two do the same to a channel, such as naming it or giving it a number, the later one wins: a rule that hides the news, then a rule that shows *Harbour News*, hides every news channel but that one. A rule can be turned off, which keeps it but makes it do nothing; turning a rule off or removing it undoes what it did.

A lineup has at most 200 rules. A copy of a lineup starts with a copy of its rules ([D106](decisions.md)).

## Examples

- **Hide what the household never watches:** *Hide them*, with the category `*Adult*`, or with the name `/\b(backup|test)\b/`.
- **One group for the children:** *Move them to a group* called *Children*, with the category `*Kids*`; then *Number them* from 100 with the same category.
- **Tidy names:** *Rename them* → *Replace text* ` HD` with nothing makes *Harbour News HD* plain *Harbour News*; `/\s*\((backup|alt)\)$/` with nothing takes off a mark at the end.
- **Only the HD copies:** *Hide them* with the category `Harbour*`, then *Show them* with the same category and the quality *HD*.
- **A guide for the +1 channels:** *Use another channel's guide* with the name `* +1`, by *Its name, changed*: replace `+1` with nothing, so that *Harbour News +1* shows the guide of *Harbour News*. Then *Move their guide's times* with the same name, 60 minutes later.
- **A copy without a guide:** *Use another channel's guide* with the name `Harbour News (Backup)`, from the channel named *Harbour News*.
- **One channel for its copies:** *Merge copies into one channel*, the copies having the same *Guide ID*, makes *Harbour News HD*, *Harbour News FHD* and *Harbour News* from a second source one channel, which plays the next copy when one does not start. Where copies have no guide ID, choose *Name, without HD and such*, and take a mark such as `(Backup)` out of names first. *Show first: The first in FHD* puts the FHD copy in the lineup.

The guide that a rule gives or moves is the one that devices see everywhere: on the box, in the Stalker and Xtream APIs, in the playlists' guides, in the guide's search, for recordings set from the guide, and for past programmes played from an archive.

## Keeping rules

*Live TV* → *Lineups* → *Export every lineup* writes every lineup's rules to a file, with what was changed by hand, and importing the file adds its lineups, rules and all, on this server or another ([lineup-file.md](lineup-file.md)).
