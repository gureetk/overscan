// SPDX-License-Identifier: AGPL-3.0-or-later

// The box's lineup as the page shows it: the account's favourites, the
// channels this box watched, all channels, then the lineup's groups
// (D52, D53); and the radio stations among them, for the Radio screen
// (D87).

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
  /**
   * The channels that the lists of channels show, by number: every one, or
   * with radio apart, those that are not radio stations.
   */
  channels: Channel[] = [];
  groups: GroupView[] = [];
  /** The radio stations, by number (D87). */
  stations: Channel[] = [];
  /** The Radio screen's lists: the favourite stations, all of them, then the groups that hold any. */
  radioGroups: GroupView[] = [];
  /** Radio stations show on the Radio screen alone, and not among the channels. */
  radioApart = false;
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
  private stationByNumber: { [n: string]: Channel } = {};
  /** The IDs of the channels that the lists of channels show. */
  private listed: { [id: string]: boolean } = {};
  private favourites: number[] = [];
  private favouriteSet: { [id: string]: boolean } = {};
  private recent: number[] = [];
  private sortedCache: { [key: string]: Channel[] } = {};

  set(l: Lineup): void {
    this.version = l.version;
    this.radioApart = !!l.radio_apart;
    this.channels = [];
    this.stations = [];
    this.byId = {};
    this.byNumber = {};
    this.stationByNumber = {};
    this.listed = {};
    let highest = 0;
    for (let i = 0; i < l.channels.length; i++) {
      const c = l.channels[i]!;
      this.byId[c.id] = c;
      if (c.radio) {
        this.stations.push(c);
        this.stationByNumber[c.number] = c;
        if (this.radioApart) {
          continue;
        }
      }
      this.channels.push(c);
      this.listed[c.id] = true;
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
      { key: 'all', name: 'All Channels', channels: this.channels },
    ];
    // Groups that devices see none of the channels of have nothing to show.
    this.groups = groups.concat(byGroups(l, this.channels)).filter(function (g, i) {
      return i < 3 || g.channels.length > 0;
    });
    const radio: GroupView[] = [
      { key: 'fav', name: 'Favourites', channels: [] },
      { key: 'all', name: 'All Stations', channels: this.stations },
    ];
    this.radioGroups = radio.concat(byGroups(l, this.stations)).filter(function (g, i) {
      return i < 2 || g.channels.length > 0;
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

  /** lastWatched returns the channel of the lists that this box played last, if any. */
  lastWatched(): Channel | null {
    return (this.groups.length >= 2 && this.groups[1]!.channels[0]) || null;
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

  /** find returns the channel with number n in the list of group key, or null; key radio finds a radio station. */
  find(key: string, n: number): Channel | null {
    if (key === 'radio') {
      return this.stationByNumber[n] || null;
    }
    if (!this.fromOne(key)) {
      return this.numbered(n);
    }
    const g = this.group(key);
    return (g && this.list(g)[n - 1]) || null;
  }

  /** digitsIn returns how many digits the highest number in the list of group key has. */
  digitsIn(key: string): number {
    if (key === 'radio') {
      const last = this.stations[this.stations.length - 1];
      return last ? String(last.number).length : 1;
    }
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
    // pick returns the channels of ids that the lists show, or the radio stations.
    const pick = function (ids: number[], radio: boolean): Channel[] {
      const out: Channel[] = [];
      for (let i = 0; i < ids.length; i++) {
        const c = self.byId[ids[i]!];
        if (c && (radio ? !!c.radio : self.listed[c.id])) {
          out.push(c);
        }
      }
      return out;
    };
    if (this.groups.length >= 2) {
      this.groups[0]!.channels = pick(this.favourites, false);
      this.groups[1]!.channels = pick(this.recent, false);
    }
    if (this.radioGroups.length >= 1) {
      this.radioGroups[0]!.channels = pick(this.favourites, true);
    }
    this.sortedCache = {};
  }
}

/** byGroups returns a list for each of the lineup's groups, with the channels of list that are in it. */
function byGroups(l: Lineup, list: Channel[]): GroupView[] {
  const out: GroupView[] = [];
  const byId: { [id: string]: GroupView } = {};
  for (let i = 0; i < l.groups.length; i++) {
    const g = l.groups[i]!;
    const view: GroupView = { key: 'g' + g.id, name: g.name, channels: [] };
    byId[g.id] = view;
    out.push(view);
  }
  for (let i = 0; i < list.length; i++) {
    const c = list[i]!;
    const view = byId[c.group];
    if (view) {
      view.channels.push(c);
    }
  }
  return out;
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
