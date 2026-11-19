// SPDX-License-Identifier: AGPL-3.0-or-later

// The MAG page's entry point. On a box, the page runs on the box's
// JavaScript API. In a desktop browser, it shows what it is for, unless the
// address asks for dev mode (?dev, with ?mac=... to act as another box,
// ?fail=a,b to have addresses that hold a or b fail to play, and ?hold=a,b
// to have them stand still until paused and played again).

import { App } from './app';
import { devPlatform } from './dev';
import { magPlatform } from './mag';

/** A locally administered MAC address, which no real network card has. */
const devMAC = '02:00:00:00:00:01';

/**
 * setRootSize sets the root font size from the screen height, so that the
 * layout's 45rem fill it: 16px at 720 lines (D33).
 */
function setRootSize(): void {
  const height = window.innerHeight || document.documentElement.clientHeight || 720;
  document.documentElement.style.fontSize = height / 45 + 'px';
}

/** param returns a query parameter, "" if it has no value, or null if absent. */
function param(name: string): string | null {
  const parts = location.search.replace(/^\?/, '').split('&');
  for (let i = 0; i < parts.length; i++) {
    const kv = parts[i]!.split('=');
    if (decodeURIComponent(kv[0]!) === name) {
      return kv.length > 1 ? decodeURIComponent(kv.slice(1).join('=')) : '';
    }
  }
  return null;
}

/** say shows a message instead of the page. */
function say(title: string, text: string): void {
  const root = document.getElementById('app');
  if (root) {
    root.innerHTML = '';
    const screen = document.createElement('div');
    screen.className = 'screen message';
    const box = document.createElement('div');
    box.className = 'box';
    const h = document.createElement('h1');
    h.textContent = title;
    const p = document.createElement('p');
    p.textContent = text;
    box.appendChild(h);
    box.appendChild(p);
    screen.appendChild(box);
    root.appendChild(screen);
  }
}

/** failing returns a pattern of the addresses that hold any of a list's words, such as dts,av1. */
function failing(list: string | null): RegExp | undefined {
  if (!list) {
    return undefined;
  }
  const words = list.split(',');
  const parts: string[] = [];
  for (let i = 0; i < words.length; i++) {
    if (words[i]) {
      parts.push(words[i]!.replace(/[^A-Za-z0-9_-]/g, ''));
    }
  }
  return parts.length ? new RegExp('/(' + parts.join('|') + ')\\.') : undefined;
}

/** holding returns a pattern of the addresses that hold any of a list's words anywhere, such as vod. */
function holding(list: string | null): RegExp | undefined {
  const words = (list || '').split(',');
  const parts: string[] = [];
  for (let i = 0; i < words.length; i++) {
    const w = words[i]!.replace(/[^A-Za-z0-9_-]/g, '');
    if (w) {
      parts.push(w);
    }
  }
  return parts.length ? new RegExp(parts.join('|')) : undefined;
}

/** fail shows an error that stopped the page. */
function fail(text: string): void {
  say('Something went wrong', text);
}

function main(): void {
  setRootSize();
  const root = document.getElementById('app');
  if (!root) {
    return;
  }
  const stb = window.gSTB;
  const dev = param('dev');
  if (stb) {
    new App(root, magPlatform(stb)).start();
  } else if (dev !== null) {
    new App(root, devPlatform(param('mac') || devMAC, failing(param('fail')), holding(param('hold')))).start();
  } else {
    say(
      'Overscan',
      "This page is the interface for MAG set-top boxes: put this address in the box's portal settings. " +
        'To try it in this browser, add ?dev to the address.',
    );
  }
}

window.onerror = function (message, source, line) {
  fail(String(message) + (source ? ' (' + String(source).replace(/^.*\//, '') + ':' + line + ')' : ''));
  return false;
};

try {
  main();
} catch (e) {
  fail(String(e));
}
