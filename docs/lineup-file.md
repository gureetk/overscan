# Lineup files

Overscan writes every lineup to one YAML file, and adds the lineups of such a file as new lineups ([D112](decisions.md)). The file keeps what was changed in each lineup, and its rules: to keep a copy, to keep the setup in git, or to take it to another server. It is not a backup of the server: [backup.md](backup.md) says how to make one.

## Exporting and importing

- *Live TV* → *Lineups* → *Export every lineup* saves `overscan-lineups-<date>.yaml`.
- *Live TV* → *Lineups* → choose a file → *Import* adds each lineup of the file as a new lineup, and shows the first. It changes no lineup that is there. A lineup whose name another has gets a number after it, such as *Kids (2)*. No account sees the new lineups until one is chosen for it on the *Accounts* page.
- The admin API does both: `GET /api/admin/v1/lineups/file` answers with the file, and `POST /api/admin/v1/lineups/file` imports the file that it is sent ([api/admin.yaml](api/admin.yaml)).

An import is all or nothing: a file with a mistake adds nothing, and the page says what is wrong and where. A server has at most 20 lineups, so a file cannot take it past 20.

## What a file holds

```yaml
# Overscan's lineups: the groups made or changed in each, its channels changed by hand, and its rules.
# Exported from Overscan v0.5.0 on 20 November 2026 at 21:40 UTC. Importing the file adds its lineups as new ones.
kind: overscan-lineups
version: 1
lineups:
  - name: Kids
    groups:
      - name: Cartoons
        locked: true
        categories:
          - source: main
            name: "Kids: Cartoons"
    channels:
      - source: main
        key: id:granite.one
        title: Granite One HD
        number: 101
        name: Granite One
        group: Cartoons
        hidden: false
        backups:
          - source: one
            key: xtream:1001
    rules:
      - match:
          source: main
          name: "*Backup*"
        action: exclude
```

- **`kind` and `version`** say what the file is. Overscan refuses a file of a later version, which a later Overscan writes, rather than drop what it does not know.
- **`lineups`**, each with its **`name`**.
- **`groups`**: the groups that were made or changed by hand, by name, with `hidden`, `locked`, and the `categories` of the sources that they hold. The groups that Overscan makes by itself, one for each category, it makes again on import.
- **`channels`**: the channels changed by hand. A channel is known by its `source`, by name, and its `key` there, which stays the same across refreshes; `title` is its name at its source, for people who read the file, and an import does not use it. What was changed: a pinned `number`, a `name`, a `group`, `hidden` (`true` or `false` for a channel hidden or shown by hand, left out for one that follows its group), `locked`, and the `backups` that play when its stream cannot start, in order. Channels not changed by hand are not in the file: an import takes them from the sources, numbered as usual.
- **`rules`**: the lineup's rules, in order, with the fields that [rules.md](rules.md) describes. A rule's `source`, if it has one, is a source's name.

Sources are found by their names. To use a file on a server whose sources have other names, change the names in the file first. What names a source or a channel that the server lacks is left out, and the import says how much.

## What it does not hold

No address, login, token or password: not a source's, an account's or the administrator's. Sources, accounts and devices stay out of the file, and so do the lineup each account sees and the output profiles, which are a short fixed list.

## Writing one by hand

A file is YAML, and any text editor can change it. Overscan refuses what an exported file never has: YAML's anchors, aliases and merges (`&`, `*` before a name, `<<`), several documents in one file, and fields that a lineup file does not have, such as a misspelt one. Names follow the admin's limits: a lineup's at most 50 characters, a group's 100 and a channel's 200, with no control characters. A channel has at most 5 backups and a number from 1 to 999,999 that no other channel of its lineup has; a lineup has at most 200 rules; a file has at most 20 lineups and 2 MB, which hold some 14,000 channels changed by hand. Overscan also refuses a file that nests more deeply than a lineup file does, since YAML takes much longer to read one. A larger file splits into files of fewer lineups, each imported on its own.
