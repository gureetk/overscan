// SPDX-License-Identifier: AGPL-3.0-or-later

// The device secret that pairs the box with the server (D39). The page keeps
// it in a cookie and, on a box, in a file in the box's user storage too: both
// survive a restart, and either restores the other if it is lost (D38). The
// file also carries the pairing over a change of portal address between /c/
// and /stalker_portal/c/, whose cookies are apart.

import type { SecretFile } from './platform';

const cookieName = 'overscan_device_secret';
const cookieYears = 10;

function readCookie(name: string): string {
  const parts = document.cookie ? document.cookie.split('; ') : [];
  for (let i = 0; i < parts.length; i++) {
    const eq = parts[i]!.indexOf('=');
    if (eq > 0 && parts[i]!.substring(0, eq) === name) {
      try {
        return decodeURIComponent(parts[i]!.substring(eq + 1));
      } catch (e) {
        return '';
      }
    }
  }
  return '';
}

/**
 * writeCookie keeps a cookie for the page's own directory, such as /c/, so
 * that the browser sends it with the page's files and not with API calls or
 * streams.
 */
function writeCookie(name: string, value: string): void {
  const expires = new Date(new Date().getTime() + cookieYears * 365 * 24 * 3600 * 1000);
  const path = location.pathname.substring(0, location.pathname.lastIndexOf('/') + 1) || '/';
  document.cookie = name + '=' + encodeURIComponent(value) + '; expires=' + expires.toUTCString() + '; path=' + path;
}

/** readSecret returns the secret the box keeps, or "" if it has none. */
export function readSecret(file: SecretFile | null): string {
  const cookie = readCookie(cookieName);
  const saved = file ? file.load() : '';
  if (cookie) {
    if (file && saved !== cookie) {
      file.save(cookie);
    }
    return cookie;
  }
  if (saved) {
    writeCookie(cookieName, saved);
  }
  return saved;
}

/** writeSecret keeps a new secret in both places. */
export function writeSecret(file: SecretFile | null, secret: string): void {
  writeCookie(cookieName, secret);
  if (file) {
    file.save(secret);
  }
}
