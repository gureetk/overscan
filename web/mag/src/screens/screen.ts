// SPDX-License-Identifier: AGPL-3.0-or-later

// What every screen is, and what the app gives the screens.

import type { Channel, Client, Programme } from '../api';
import type { PlaybackCheck } from '../check';
import type { FilmPlayer, Playing } from '../film';
import { el, setHTML, setText } from '../dom';
import type { GuideCache } from '../guide';
import { arrows } from '../icons';
import type { Press } from '../keys';
import type { LibraryModel } from '../library';
import type { LineupModel } from '../lineup';
import type { PickItem } from '../overlays';
import type { Platform, Rect } from '../platform';
import type { Player } from '../player';
import type { ProfilesModel } from '../profiles';
import type { Recordings } from '../recordings';
import type { SettingsModel } from '../settings';
import { clock, date, now } from '../time';

export type ScreenName =
  | 'home'
  | 'live'
  | 'watch'
  | 'guide'
  | 'search'
  | 'settings'
  | 'movies'
  | 'series'
  | 'title'
  | 'episodes'
  | 'film'
  | 'check'
  | 'radio'
  | 'recordings'
  | 'message';

/**
 * Where a screen wants the video: in a window, full screen, or nowhere; or
 * for a radio station, which has no picture, nowhere while it plays (D87).
 */
export type VideoArea = Rect | 'full' | 'none' | 'audio';

export interface Ctx {
  platform: Platform;
  client: Client;
  lineup: LineupModel;
  guide: GuideCache;
  settings: SettingsModel;
  /** The account's profiles, and the one the box plays as (D94). */
  profiles: ProfilesModel;
  player: Player;
  /** Films and series (D63). */
  movies: LibraryModel;
  series: LibraryModel;
  film: FilmPlayer;
  /** The playback check (D70). */
  check: PlaybackCheck;
  /** The household's recordings to come (D89). */
  recordings: Recordings;
  /** The server's version, the box's label in the admin, and when its account ends, in Unix seconds, or 0 (D95). */
  server: { version: string; label: string; ends: number };
  /** go shows another screen, which Back leaves. */
  go(name: ScreenName, arg?: any): void;
  back(): void;
  toast(text: string): void;
  /**
   * watch plays a channel of a list, in the screen's window or full screen;
   * key is the group the list is, or "" for another list.
   */
  watch(c: Channel, list: Channel[], full: boolean, key: string): void;
  toggleFavourite(c: Channel): void;
  /** playFilm plays a film or an episode full screen, from a time in seconds. */
  playFilm(p: Playing, from: number): void;
  /** playProgramme plays a past programme of a channel full screen, from its source's archive (D81). */
  playProgramme(c: Channel, p: Programme): void;
  pick(title: string, items: PickItem[], chosen: number, done: (index: number) => void): void;
  /**
   * unlock asks for the parental PIN before something locked shows, unless
   * the page keeps it, and checks it with the server (D85); done says
   * whether it may show.
   */
  unlock(what: string, done: (ok: boolean) => void): void;
  /** switchProfile asks who is watching, and switches the box to that profile, with its PIN if it has one (D94). */
  switchProfile(): void;
  /** videoChanged says that the screen wants the video somewhere else, or overlays changed. */
  videoChanged(): void;
}

export interface Screen {
  el: HTMLElement;
  /** Shows the screen; arg is what go passed. */
  enter(arg: any): void;
  leave(): void;
  /** Handles a key, and says whether it did. */
  key(p: Press): boolean;
  /** Called every few seconds, for the clock and progress. */
  tick(): void;
  /** The lineup has been loaded again. */
  lineupChanged(): void;
  /** The player started a channel, or stopped. */
  playback(): void;
  /** Where the video should be while the screen shows. */
  video(): VideoArea;
  /** Whether typing a channel number tunes to it here. */
  tunes: boolean;
  /** tune shows channel c, typed by its number. */
  tune?(c: Channel): void;
  /** The group whose numbers a typed number is, if not the lineup's. */
  numbering?(): string;
  /** Whether anything of the screen shows over full-screen video. */
  overVideo?(): boolean;
}

/** Clock is the date and time at the top of a screen. */
export class Clock {
  private date: HTMLElement | null;
  private time: HTMLElement;

  constructor(parent: HTMLElement, withDate: boolean) {
    const box = el('div', 'clock', parent);
    this.date = withDate ? el('span', 'date', box) : null;
    this.time = el('span', 'time', box);
    this.tick();
  }

  tick(): void {
    const t = now();
    if (this.date) {
      setText(this.date, date(t));
    }
    setText(this.time, clock(t));
  }
}

/** Hint is a key hint for the foot of a screen. */
export interface Hint {
  /** red, green, yellow or blue for a colour key; otherwise the key's label, or arrows such as "lr". */
  key: string;
  text: string;
}

const colours: { [k: string]: boolean } = { red: true, green: true, yellow: true, blue: true };

/** hints writes the markup of a row of key hints. */
export function hints(list: Hint[]): string {
  let out = '';
  for (let i = 0; i < list.length; i++) {
    const h = list[i]!;
    if (colours[h.key]) {
      out += '<span class="hint"><span class="swatch ' + h.key + '"></span>' + escape(h.text) + '</span>';
    } else {
      const label = /^[lrud]+$/.test(h.key) ? arrows(h.key) : escape(h.key);
      out += '<span class="hint"><span class="keycap">' + label + '</span>' + escape(h.text) + '</span>';
    }
  }
  return out;
}

/** Footer is the row of key hints at the foot of a screen: colour keys left, others right. */
export class Footer {
  private left: HTMLElement;
  private right: HTMLElement;

  constructor(parent: HTMLElement) {
    const box = el('div', 'footer', parent);
    this.left = el('div', 'hints left', box);
    this.right = el('div', 'hints right', box);
  }

  set(left: Hint[], right: Hint[]): void {
    setHTML(this.left, hints(left));
    setHTML(this.right, hints(right));
  }

  /** setRightText puts plain text on the right, such as the box's name. */
  setRightText(text: string): void {
    setHTML(this.right, '<span class="hint">' + escape(text) + '</span>');
  }
}

/** escape makes text safe to put in markup. */
export function escape(text: string): string {
  return text.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/** rect returns e's place on the page, for the video window. */
export function rectOf(e: HTMLElement): Rect {
  const r = e.getBoundingClientRect();
  return { x: r.left, y: r.top, w: r.width, h: r.height };
}
