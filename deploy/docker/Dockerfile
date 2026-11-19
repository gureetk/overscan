# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Overscan's container image, built from the binary of a release archive,
# which holds this file. In the unpacked archive:
#
#   docker compose up -d --build
#
# docs/install-docker.md says more. The image runs Overscan as its own user,
# with its data in a volume at /var/lib/overscan and no configuration file:
# settings come from OVERSCAN_* environment variables, or from a file mounted
# at /etc/overscan/overscan.yaml.
FROM debian:trixie-slim

# ffmpeg repairs streams (D57); the certificates let Overscan fetch sources
# over HTTPS.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates ffmpeg \
	&& rm -rf /var/lib/apt/lists/*

RUN useradd --system --uid 10001 --user-group --home-dir /var/lib/overscan --no-create-home \
		--shell /usr/sbin/nologin overscan \
	&& install -d -m 0700 -o overscan -g overscan /var/lib/overscan \
	&& install -d -m 0755 /etc/overscan

COPY --chmod=0755 overscan /usr/local/bin/overscan

USER overscan
WORKDIR /var/lib/overscan
VOLUME /var/lib/overscan
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/overscan"]
