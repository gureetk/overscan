# Contributing

Outside contributions are not open yet (see the README). The rules below already apply to every commit, the maintainer's included.

## Before you start

- Read [docs/clean-room.md](docs/clean-room.md). It says which sources you may use for anything touching the Stalker protocol or the Xtream API, and what to do if you have seen proprietary Infomir or IPTV panel code.
- The plan is in [docs/plan.md](docs/plan.md), and the reasons behind each decision are in [docs/decisions.md](docs/decisions.md).

## How changes land

- One topic per branch and one pull request per branch. The maintainer squash-merges.
- Titles describe the change in neutral terms. Descriptions say what changed and why.
- Fixtures, tests and examples are fictional: no real portal addresses, MAC addresses, credentials, provider playlists or copyrighted media.

## Sign-off

Every commit is signed off to certify the [Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change, or otherwise have the right to submit it under the project's licence.

    git commit -s

This adds a `Signed-off-by: Your Name <you@example.com>` line, which must match the commit's author. CI checks every commit in a pull request, and `tools/check-dco.sh origin/main HEAD` runs the same check locally.

## Licence headers

Every source file starts with an SPDX licence identifier: `AGPL-3.0-or-later`, except under `docs/protocol/`, `conformance/` and `docs/logo/`, which are `Apache-2.0`. Files vendored from other projects, under `web/admin/static/vendor/`, keep their own licence and name it in the same way; [web/admin/README.md](web/admin/README.md) lists them. The MAG page's fonts, under `web/mag/src/assets/fonts/`, are under the SIL Open Font License 1.1, whose text is beside them ([web/mag/README.md](web/mag/README.md)).

| File type | Header |
| --- | --- |
| Go, JavaScript, TypeScript | `// SPDX-License-Identifier: AGPL-3.0-or-later` |
| Shell, YAML | `# SPDX-License-Identifier: AGPL-3.0-or-later`, after the `#!` line in scripts |
| CSS | `/* SPDX-License-Identifier: AGPL-3.0-or-later */` |
| SQL | `-- SPDX-License-Identifier: AGPL-3.0-or-later` |
| HTML | `<!-- SPDX-License-Identifier: AGPL-3.0-or-later -->`, right after `<!doctype html>` |

`tools/check-spdx.sh` checks this, and CI runs it.

## Checks

CI runs these on every pull request. Run them before you push:

    gofmt -l .
    go mod tidy -diff
    go vet ./...
    go test ./...
    tools/check-spdx.sh
    tools/check-es5.sh

The stream repair tests run ffmpeg if it is installed, and skip what needs it otherwise. To check the Docker image as CI does, lay out a release archive's files and run `tools/check-image.sh` on them (see the `docker` job in `.github/workflows/ci.yml`).

Browser scripts under `web/` must be ES5, since a set-top box's browser may support nothing newer. `tools/check-es5.sh` parses them as ES5; it needs Node.js. The admin, under `web/admin/`, runs in desktop browsers and is left out.

The MAG page in `web/mag/` is written in TypeScript and built into `web/mag/dist/`, which is committed, so that building Overscan needs no Node.js. After changing it, rebuild and commit `dist/` with the sources; CI fails if they differ. `web/mag/README.md` explains the build and how to try the page in a desktop browser:

    cd web/mag
    npm ci --ignore-scripts
    npm run check

[docs/testing.md](docs/testing.md) covers the conformance suite, fuzzing, the soak test, upgrades and the checklists for the box.

The `toolchain` line in `go.mod` pins the Go release CI builds with; the `go` line is the oldest release the code builds with. CI also runs `govulncheck` to catch known vulnerabilities in dependencies.

## Database migrations

The schema lives in `internal/store/migrations/`, one SQL file per version, named `NNNN_description.sql` and numbered without gaps. Overscan applies the ones a database lacks when it starts, each in a transaction, once it has backed the database up ([D117](docs/decisions.md)). Tables are `STRICT`, and times are stored as UTC text in RFC 3339 form.

An upgrade never loses or changes what a household set up ([D115](docs/decisions.md)), so a migration that changes how something is kept carries it over to the new form, and drops nothing that a household made. With each new migration:

- add the line that `sha256sum` gives for it to `internal/store/testdata/migrations.sha256`. Once a migration has been released, never edit it: change the schema in a new one. `TestMigrationsAreUnchanged` checks this.
- write its upgrade test, `TestMigrationNNNN…` in `internal/store`: make a database with the schema before it (`open` with the migrations before it), put in what a household sets up that the migration touches, apply it, and check that all of it is still there and the same. `TestProfilesMigration` is an example, and `TestNewMigrationsHaveUpgradeTests` checks that the test exists.

CI's *Upgrades* job then upgrades a household from every full release since v0.3.0 to the change ([docs/testing.md](docs/testing.md#upgrades)).

## Dependencies

Keep them few. A new dependency needs a reason in the pull request, a licence compatible with AGPL-3.0-or-later, and signs of active maintenance.

## Releases

The Release workflow builds release archives with the toolchain pinned in `go.mod` and publishes them. To make a release, run it from the **Actions** tab with a version such as `v0.1.0`: it tags the head of `main` and publishes the release. Pushing a tag works too, and so does publishing a release from the **Releases** page, which gets the archives added to it. Versions `v0.0.x` and versions with a hyphen, such as `v0.1.0-rc.1`, are marked as pre-releases.
