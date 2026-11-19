// SPDX-License-Identifier: AGPL-3.0-or-later

// The parental PIN (D55) that the viewer typed last, kept for a while so
// that moving between locked channels, and their past programmes, does not
// ask for it each time.

/** How long a right PIN is kept. */
const pinKeepMs = 10 * 60 * 1000;

let kept = '';
let until = 0;

/** keptPIN returns the PIN that the page keeps, or "" if it keeps none. */
export function keptPIN(): string {
  return kept && new Date().getTime() < until ? kept : '';
}

/** keepPIN keeps a PIN that the server took. */
export function keepPIN(pin: string): void {
  kept = pin;
  until = new Date().getTime() + pinKeepMs;
}

/** forgetPIN drops the PIN that the page keeps. */
export function forgetPIN(): void {
  kept = '';
  until = 0;
}
