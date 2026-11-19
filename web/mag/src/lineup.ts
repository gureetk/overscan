// SPDX-License-Identifier: AGPL-3.0-or-later

// The box's lineup as the page shows it: the account's favourites, the
// channels this box watched, all channels, then the lineup's groups
// (D52, D53).

import type { Channel, Lineup } from './api';

/** How many channels the server keeps in a box's recent list. */
const maxRecent = 30;

export interface GroupView {
  /** fav, recent, all, or g<id> for a group of the lineup. */
  key: string;
  name: string;
  /** By number, or for the recent list, the latest first. */
  channels: Channel[];
}

export class LineupModel {
  version = '';
  /** By number. */
  channels: Channel[] = [];
  groups: GroupView[] = [];
  /** How many digits the highest channel number has. */
  maxDigits = 1;
  /** Lists are by name rather than by number; the recent list stays as it is. */
  byName = false;
  /** How the lists number their channels: the box's setting (D56). */
  numbering: () => string = function () {
    return 'lineup';
  };

  private byId: { [id: string]: Channel } = {};
  private byNumber: { [n: string]: Channel } = {};
  private favourites: number[] = [];
  private favouriteSet: { [id: string]: boolean } = {};
  private recent: number[] = [];
  private sortedCache: { [key: string]: Channel[] } = {};

  set(l: Lineup): void {
    this.version = l.version;
    this.channels = l.channels;
    this.byId = {};
    this.byNumber = {};
    let highest = 0;
    for (let i = 0; i < l.channels.length; i++) {
      const c = l.channels[i]!;
      this.byId[c.id] = c;
      this.byNumber[c.number] = c;
      highest = Math.max(highest, c.number);
    }
    this.maxDigits = String(highest).length;
    this.favourites = this.known(l.favourites || []);
    this.favouriteSet = {};
    for (let i = 0; i < this.favourites.length; i++) {
      this.favouriteSet[this.favourites[i]!] = true;
    }
    this.recent = this.known(l.recent || []);

    const groups: GroupView[] = [
      { key: 'fav', name: 'Favourites', channels: [] },
      { key: 'recent', name: 'Recently Watched', channels: [] },
      { key: 'all', name: 'All Channels', channels: l.channels },
    ];
    const byGroup: { [id: string]: GroupView } = {};
    for (let i = 0; i < l.groups.length; i++) {
      const g = l.groups[i]!;
      const view: GroupView = { key: 'g' + g.id, name: g.name, channels: [] };
      byGroup[g.id] = view;
      groups.push(view);
    }
    for (let i = 0; i < l.channels.length; i++) {
      const c = l.channels[i]!;
      const view = byGroup[c.group];
      if (view) {
        view.channels.push(c);
      }
    }
    // Groups that devices see none of the channels of have nothing to show.
    this.groups = groups.filter(function (g, i) {
      return i < 3 || g.channels.length > 0;
    });
    this.rebuild();
  }

  channel(id: number): Channel | null {
    return this.byId[id] || null;
  }

  numbered(n: number): Channel | null {
    return this.byNumber[n] || null;
  }

  isFavourite(id: number): boolean {
    return !!this.favouriteSet[id];
  }

  setFavourite(id: number, on: boolean): void {
    if (on === this.isFavourite(id)) {
      return;
    }
    if (on) {
      this.favouriteSet[id] = true;
      this.favourites.push(id);
    } else {
      delete this.favouriteSet[id];
      this.favourites = this.favourites.filter(function (f) {
        return f !== id;
      });
    }
    this.rebuild();
  }

  /** played puts a channel first in the recent list, as the server does. */
  played(id: number): void {
    this.recent = [id].concat(
      this.recent.filter(function (r) {
        return r !== id;
      }),
    );
    if (this.recent.length > maxRecent) {
      this.recent.length = maxRecent;
    }
    this.rebuild();
  }

  /** lastWatched returns the channel this box played last, if any. */
  lastWatched(): Channel | null {
    return this.recent.length ? this.channel(this.recent[0]!) : null;
  }

  favouriteChannels(): Channel[] {
    return this.groups[0]!.channels;
  }

  group(key: string): GroupView | null {
    for (let i = 0; i < this.groups.length; i++) {
      if (this.groups[i]!.key === key) {
        return this.groups[i]!;
      }
    }
    return null;
  }

  /** list returns a group's channels in the order the lists show them. */
  list(g: GroupView): Channel[] {
    if (!this.byName || g.key === 'recent') {
      return g.channels;
    }
    let sorted = this.sortedCache[g.key];
    if (!sorted) {
      sorted = g.channels.slice().sort(function (a, b) {
        const x = a.name.toLowerCase();
        const y = b.name.toLowerCase();
        return x < y ? -1 : x > y ? 1 : a.number - b.number;
      });
      this.sortedCache[g.key] = sorted;
    }
    return sorted;
  }

  /**
   * fromOne says whether the list of group key numbers its channels from 1
   * in their order, rather than with the lineup's numbers (D56). All
   * Channels and Recently Watched always show the lineup's numbers.
   */
  fromOne(key: string): boolean {
    const mode = this.numbering();
    if (mode === 'groups') {
      return key === 'fav' || key.charAt(0) === 'g';
    }
    return mode === 'favourites' && key === 'fav';
  }

  /** shownNumber returns the number that the list of group key shows for c, its item i. */
  shownNumber(c: Channel, key: string, i: number): number {
    return this.fromOne(key) ? i + 1 : c.number;
  }

  /** find returns the channel with number n in the list of group key, or null. */
  find(key: string, n: number): Channel | null {
    if (!this.fromOne(key)) {
      return this.numbered(n);
    }
    const g = this.group(key);
    return (g && this.list(g)[n - 1]) || null;
  }

  /** digitsIn returns how many digits the highest number in the list of group key has. */
  digitsIn(key: string): number {
    const g = this.fromOne(key) ? this.group(key) : null;
    return g ? String(Math.max(1, g.channels.length)).length : this.maxDigits;
  }

  setByName(on: boolean): void {
    this.byName = on;
    this.sortedCache = {};
  }

  private known(ids: number[]): number[] {
    const self = this;
    return ids.filter(function (id) {
      return !!self.byId[id];
    });
  }

  private rebuild(): void {
    const self = this;
    const pick = function (ids: number[]): Channel[] {
      const out: Channel[] = [];
      for (let i = 0; i < ids.length; i++) {
        const c = self.byId[ids[i]!];
        if (c) {
          out.push(c);
        }
      }
      return out;
    };
    if (this.groups.length >= 2) {
      this.groups[0]!.channels = pick(this.favourites);
      this.groups[1]!.channels = pick(this.recent);
    }
    this.sortedCache = {};
  }
}

/** indexOf returns where channel id is in list, or -1. */
export function indexOf(list: Channel[], id: number): number {
  for (let i = 0; i < list.length; i++) {
    if (list[i]!.id === id) {
      return i;
    }
  }
  return -1;
}

/** isUHD says whether a channel's name says it is in 4K. */
export function isUHD(c: Channel): boolean {
  return /\b(4K|UHD)\b/i.test(c.name);
}
