# Clean-room policy

Overscan implements a protocol that Infomir's proprietary middleware also implements. It also reads the Xtream API, which proprietary IPTV panels serve, and will serve that API itself from M6. This page says how we document and build these protocols so that Overscan stays an independent work. It is a project policy, not legal advice; a short consultation with a lawyer before a public release is sensible.

## Principle

Behaviour can be documented from what is public or observable. Proprietary code cannot be copied, translated or paraphrased. When in doubt, leave it out and ask.

## Allowed sources

- Infomir's public documentation: the wiki, the JavaScript API reference, the public REST reference and published example portals, under their own terms.
- Community documentation and forum posts, as descriptions of behaviour. Check the licence before copying any text or code from them.
- Open-source clients and servers, under their licences, with their licence conditions respected. The maintainer's own client code is a source too.
- Traffic from your own devices talking to your own Overscan server, and from your own devices talking to portals you are entitled to use. Capture only what you need, and redact MAC addresses, serial numbers, tokens and credentials before sharing anything.
- Observation of how a real box behaves with your own server, including what its web UI asks for.

## Not allowed

- Leaked, stolen or otherwise unauthorised copies of proprietary server code, firmware sources or documentation.
- The source of Infomir's old Stalker Middleware. Copies survive in forks of a GitHub repository whose original now holds only a README. Some describe the code as open source, but it carries no licence, so it counts as proprietary here.
- Code, configuration or data extracted from firmware images, or from a portal install you are not licensed to inspect, and anything derived from them.
- Leaked, decoded or otherwise unauthorised copies of IPTV panel code (Xtream Codes, XUI and the like), and anything derived from them. That includes write-ups made by reading such code: some "API documentation" online says it was produced by analysing decoded panel source.
- Decompiling, disassembling or otherwise reverse engineering Infomir software where the licence forbids it.
- Copying the look, text or assets of Infomir's portal skins, or any Infomir mark or artwork.
- Real portal addresses, accounts, MAC addresses or content in the repository, including in tests and fixtures. Fixtures are fictional.

## Provenance

Every protocol behaviour that Overscan implements or documents should say where it came from, in one of three levels:

| Level | Meaning |
| --- | --- |
| **Documented** | Written down in a public source, which the note names. |
| **Observed** | Seen in traffic from a device or client the contributor owns. Say which model and firmware, or which client and version. |
| **Assumed** | Inferred, or taken from a client written against an unverified description. Treat as a hypothesis and mark it as such. |

The notes in [protocol/stalker-protocol-notes.md](protocol/stalker-protocol-notes.md) start out at the **Assumed** level: they were derived from a client implementation written by the maintainer and from community documentation, and that client has only been tested against fakes. Entries move to **Observed** when a real box or client confirms them, and the notes are corrected where the real thing disagrees.

The notes in [protocol/xtream-api-notes.md](protocol/xtream-api-notes.md) start at the **Documented** and **Assumed** levels: they come from open-source clients and servers and from public write-ups, each named. Entries move to **Observed** when Overscan meets a real panel, which the note describes in general terms, without naming the provider.

The notes in [protocol/jellyfin-api-notes.md](protocol/jellyfin-api-notes.md) are **Documented** from Jellyfin's own source code, which is open source (GPL-2.0): they describe its behaviour in our words and copy none of its code.

The notes in [protocol/plex-api-notes.md](protocol/plex-api-notes.md) are **Documented** from Plex's own API documentation and from an OpenAPI description of that API published under the MIT licence. Plex Media Server is not open source, and none of its code is read.

The notes in [protocol/catchup-notes.md](protocol/catchup-notes.md) are **Documented** from the public documentation of two players that read catch-up from playlists, one of them open source, whose code was not read, and from a streaming server's own documentation; Overscan's choices where they leave room are marked as such.

The notes in [protocol/playlist-notes.md](protocol/playlist-notes.md) are **Documented** from the public documentation of the same two players, and not from their code.

## Contributor rules

- Do not paste proprietary code, or text from it, into issues, pull requests, comments or commit messages, even to ask a question.
- If you have read proprietary Infomir server code or have had access to firmware sources, say so before contributing, and stay off the Stalker protocol modules and notes. If you have read proprietary IPTV panel code, say so too, and stay off the Xtream adapter, the Xtream output and their notes. You can still help with everything else, such as the other source adapters, the admin UI and packaging.
- Describe behaviour in your own words, and add the provenance level and source to what you write.
- Captures contributed as evidence must be redacted and must come from your own devices.
- Sign off every commit (`Signed-off-by`) to certify the Developer Certificate of Origin. That includes the statement that you have the right to submit the work under the project's licence.

## Trademarks

"Stalker", "Ministra", "MAG" and "Infomir" belong to their owners, as do the names of IPTV panels such as Xtream Codes. Overscan uses them only to say what it is compatible with. They are not part of Overscan's name, logo or interface, and the interface does not imitate Infomir's design. Overscan is not affiliated with or endorsed by Infomir, or by the makers of any IPTV panel.

## If a problem is found

If something in the repository looks like proprietary code or data, open a private report rather than a public issue. We will remove it, rewrite the affected part from allowed sources and record what happened in [decisions.md](decisions.md).
