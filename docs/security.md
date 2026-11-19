# Security

How Overscan protects a household's server, what it leaves to the network it runs on, and what the review before v0.1.0 checked. [`SECURITY.md`](../SECURITY.md) says how to report a vulnerability, and the plan's section 11 is the design.

## The model

Overscan runs on a home network for the household's own devices. It trusts the administrator, who signs in to the admin, and the sources that the administrator adds, as far as fetching what they point to. Everything else is untrusted: devices that have not been approved, whatever a source sends, and the rest of the network.

## What is in place

| Area | In place |
|---|---|
| Network | Plain HTTP on port 8080, for the home network. `admin_listen` can give the admin an address of its own, such as `127.0.0.1:8081` reached over SSH (D44). Do not expose Overscan to the internet; if you must, put it behind a reverse proxy with TLS. |
| Devices | A new device waits for approval. The MAG page pairs with a secret that the server keeps hashed, and the Stalker API refuses the MAC address of a paired box (D39). Stalker tokens are kept hashed. |
| Admin | An argon2id password; sessions in the database behind `HttpOnly`, `SameSite=Strict` cookies, `Secure` when the request came over TLS; CSRF tokens on every change; sign-ins slowed after failures; a content security policy with no inline scripts, `frame-ancestors 'none'` and `nosniff`. |
| Secrets | Source addresses and logins, and stream addresses that hold them, encrypted with AES-256-GCM under a key file kept outside the database (D46). They never reach a device, a page, a log or an error message, and ffmpeg never sees them. Tokens and secrets are compared in constant time. |
| Files | Readable by their owner only, whatever the umask: the database and its companions, the key file, backups (0600 in 0700 folders). The systemd unit adds its sandbox: no privileges or capabilities, a read-only system, private `/tmp` and devices, three address families, no new namespaces, no writable and executable memory. The Docker image runs as its own user on a read-only file system with no capabilities. |
| Streams | Links signed with HMAC, bound to the device and the channel, valid for a minute (D21). The hub proxies every stream, so upstream addresses stay on the server (D20), and keeps each source's limit (D36). |
| Recordings | Off until the administrator gives them room (D89). A recording plays through the hub as any stream does, within its source's limit, and never ends a device's stream. Files are written only in the recordings folder, under names made of a programme's and a channel's letters, digits and a few marks, never a path from a source, a guide or a device; the folder's room is a cap that recordings stop at. A box schedules and calls off only its own household's recordings; deleting is the admin's. |
| Parental PIN | Hashed with argon2id; after five wrong PINs in 15 minutes a box waits five minutes (D55). |
| Content from sources | Size caps on everything read: playlists 256 MiB, HLS playlists 2 MiB, logos 4 MiB and 25 megapixels, guides, request bodies. The parsers of M3U, XMLTV and HLS playlists, the stream analyser and the repeat filter are fuzzed. Logos are converted by Overscan and SVG is refused (D54). |
| ffmpeg | Run with an argument list, never a shell; it reads and writes pipes only (`-protocol_whitelist pipe`), with an empty environment; at most eight at once; stopped with the stream, and with Overscan (D57). |
| Logs | No addresses, logins, tokens, serial numbers or signatures. MAC addresses do appear, so that a device can be approved from the log; the support bundle keeps only their maker's half. |
| Dependencies | Four direct Go modules besides the standard library, pinned in `go.sum`, checked by `govulncheck` in CI. No telemetry. |

## Known limits in v0.1.0

- Overscan speaks plain HTTP; TLS is a reverse proxy's job. Behind one, the admin's cookie lacks the `Secure` flag, since Overscan sees plain HTTP.
- A source can make Overscan fetch any http or https address, addresses on the home network included: redirects, HLS segments and keys, logos. What comes back goes only to the household's devices, but the request is made. An option to refuse private addresses for a source is planned (plan, section 11).
- An M3U source can be a local file, which the admin can name; Overscan reads it as its own user, and only playlist entries come out of it.
- The probe page's report address takes reports from anyone on the network, at most 100 of 256 KiB each.
- Once a device is approved, its MAC address is all that a Stalker client needs, as the protocol has it. The MAG page adds its pairing secret.

## The review before v0.1.0

Each row of the plan's section 11 was checked against the code. What changed:

- **Fuzzing.** New fuzz tests for HLS playlists, the stream analyser and the repeat filter, beside the M3U and XMLTV ones. Each fuzzer ran for at least 45 seconds without a crash. One finding: an initialisation vector that did not parse left part of a value behind. It did no harm, since such a playlist is not relayed, but it is now dropped.
- **Files.** SQLite created the database's shared-memory index with the umask's mode, so outside the systemd unit others could read it. Overscan now sets its umask to 077 (#62).
- **ffmpeg** got its pipes, allowlist, empty environment and cap with stream repair (#60).
- **Backups** are private and never overwrite a file (#62), and the support bundle is redacted (#63).

Candidates for later: refusing private addresses per source; a system call filter in the systemd unit (`SystemCallFilter=@system-service`), once tested in a container.
