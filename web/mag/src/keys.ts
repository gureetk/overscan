// SPDX-License-Identifier: AGPL-3.0-or-later

// Remote control keys, as the box reports them in keydown events (Infomir's
// key code table: https://soft.infomir.com/stbapi/JS/v343/tutorial-keys.html).
// A desktop keyboard sends the same codes for most of them, so dev mode
// needs few mappings of its own; the README lists them.

export type Key =
  | 'up'
  | 'down'
  | 'left'
  | 'right'
  | 'ok'
  | 'back'
  | 'exit'
  | 'pageUp'
  | 'pageDown'
  | 'channelNext'
  | 'channelPrev'
  | 'digit'
  | 'volumeUp'
  | 'volumeDown'
  | 'mute'
  | 'info'
  | 'stop'
  | 'red'
  | 'green'
  | 'yellow'
  | 'blue'
  | 'menu'
  | 'tv'
  | 'guide'
  | 'audio'
  | 'char';

export interface Press {
  key: Key;
  /** The digit, for the digit key. */
  digit: number;
  /** The character, in lower case, for a letter or the space from a keyboard. */
  char: string;
}

function press(key: Key): Press {
  return { key: key, digit: 0, char: '' };
}

/** keyOf returns the remote key that e reports, or null for another key. */
export function keyOf(e: KeyboardEvent): Press | null {
  const code = e.keyCode || e.which;
  // The function keys carry the colour keys and the box's own keys,
  // whatever modifier comes with them. Many TV browsers send 403 to 406
  // for the colour keys, 457 for Info and 458 for Guide instead.
  switch (code) {
    case 112:
    case 403:
      return press('red');
    case 113:
    case 404:
      return press('green');
    case 114:
    case 405:
      return press('yellow');
    case 115:
    case 406:
      return press('blue');
    case 457:
      return press('info');
    case 119:
    case 458:
      return press('guide');
    case 121:
      return press('tv');
    case 122:
      return press('menu');
  }
  if (e.altKey) {
    switch (code) {
      case 192:
        return press('mute');
      case 89: // Alt+Y
        return press('info');
      case 83: // Alt+S
        return press('stop');
      case 71: // Alt+G
        return press('audio');
    }
    return null;
  }
  if (e.ctrlKey || e.metaKey) {
    return null;
  }
  if (code >= 48 && code <= 57 && !e.shiftKey) {
    return { key: 'digit', digit: code - 48, char: '' };
  }
  if (code >= 96 && code <= 105) {
    return { key: 'digit', digit: code - 96, char: '' }; // a keyboard's number pad
  }
  switch (code) {
    case 38:
      return press('up');
    case 40:
      return press('down');
    case 37:
      return press('left');
    case 39:
      return press('right');
    case 13:
      return press('ok');
    case 8:
      return press('back');
    case 27:
      return press('exit');
    case 33:
      return press('pageUp');
    case 34:
      return press('pageDown');
    case 9:
      return press(e.shiftKey ? 'channelPrev' : 'channelNext');
    case 107:
      return press('volumeUp');
    case 109:
      return press('volumeDown');
    case 36: // Home, in dev mode
      return press('menu');
  }
  // Letters and the space from a keyboard, for typing a search.
  if ((code >= 65 && code <= 90) || code === 32) {
    return { key: 'char', digit: 0, char: code === 32 ? ' ' : String.fromCharCode(code).toLowerCase() };
  }
  return null;
}
