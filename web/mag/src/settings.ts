// SPDX-License-Identifier: AGPL-3.0-or-later

// The box's own settings, which the server keeps (D52): Home's look and
// picture, the accent colour, the clock, how the lists number channels
// (D56), when Live TV shows a group's channels (D62), and how a recording
// started on a channel ends (D99).

import names from 'assets';
import type { Client, Settings } from './api';
import { useTwelveHour } from './time';

export interface Choice {
  value: string;
  label: string;
}

export const homeChoices: Choice[] = [
  { value: 'simple', label: 'Simple' },
  { value: 'picture', label: 'With a Picture' },
];

export const accentChoices: Choice[] = [
  { value: 'teal', label: 'Teal' },
  { value: 'white', label: 'White' },
  { value: 'blue', label: 'Blue' },
  { value: 'orange', label: 'Orange' },
];

export const clockChoices: Choice[] = [
  { value: '24', label: '24-Hour' },
  { value: '12', label: '12-Hour' },
];

/** How the lists number their channels (D56). */
export const numbersChoices: Choice[] = [
  { value: 'lineup', label: 'As in the Lineup' },
  { value: 'favourites', label: 'Favourites from 1' },
  { value: 'groups', label: 'Each Group from 1' },
];

/** When Live TV shows the channels of a group in the groups list (D62). */
export const groupsChoices: Choice[] = [
  { value: 'preview', label: 'Preview as You Move' },
  { value: 'ok', label: 'Open with OK' },
];

/** How a recording that the box starts on a channel ends (D99). */
export const recordChoices: Choice[] = [
  { value: 'household', label: 'Household Default' },
  { value: 'programme', label: 'When the Programme Ends' },
  { value: 'stopped', label: 'When I Stop It' },
];

/** The page's own pictures for Home, the first the one a new box gets. */
export const pictureChoices: Choice[] = [
  { value: 'dusk', label: 'Dusk' },
  { value: 'aurora', label: 'Aurora' },
  { value: 'coast', label: 'Coast' },
  { value: 'dunes', label: 'Dunes' },
];

export const defaults: Settings = { home: 'simple', picture: 'dusk', accent: 'teal', clock: '24', numbers: 'lineup', groups: 'preview', record: 'household' };

/** pictureURL returns the address of a picture for Home, the first for a name the page does not know. */
export function pictureURL(name: string): string {
  return names['pictures/' + name + '.jpg'] || names['pictures/' + pictureChoices[0]!.value + '.jpg'] || '';
}

/** label returns the label of value among choices. */
export function label(choices: Choice[], value: string): string {
  for (let i = 0; i < choices.length; i++) {
    if (choices[i]!.value === value) {
      return choices[i]!.label;
    }
  }
  return choices[0]!.label;
}

/** cycle returns the choice delta places from value, wrapping around. */
export function cycle(choices: Choice[], value: string, delta: number): string {
  let i = 0;
  for (let k = 0; k < choices.length; k++) {
    if (choices[k]!.value === value) {
      i = k;
    }
  }
  const n = choices.length;
  return choices[(((i + delta) % n) + n) % n]!.value;
}

export class SettingsModel {
  current: Settings = {
    home: defaults.home,
    picture: defaults.picture,
    accent: defaults.accent,
    clock: defaults.clock,
    numbers: defaults.numbers,
    groups: defaults.groups,
    record: defaults.record,
  };
  private client: Client;
  private saveTimer = 0;
  private unsaved: { [k: string]: string } = {};

  constructor(client: Client) {
    this.client = client;
    this.apply();
  }

  /** load asks the server for the box's settings, and applies them. */
  load(done: () => void): void {
    const self = this;
    this.client.call<Settings>('GET', 'settings', null, function (err, s) {
      if (!err && s) {
        // A server older than a setting leaves it out: keep its default.
        for (const k in s) {
          (self.current as any)[k] = (s as any)[k];
        }
        self.apply();
      }
      done();
    });
  }

  /** set changes a setting at once, and saves it on the server a moment later. */
  set(name: keyof Settings, value: string): void {
    (this.current as any)[name] = value;
    this.unsaved[name] = value;
    this.apply();
    const self = this;
    clearTimeout(this.saveTimer);
    this.saveTimer = setTimeout(function () {
      const body = self.unsaved;
      self.unsaved = {};
      self.client.call<Settings>('PATCH', 'settings', body, function () {});
    }, 800);
  }

  /** apply puts the settings into effect: the accent's class on the page, and the clock. */
  apply(): void {
    const root = document.documentElement;
    root.className = (' ' + root.className + ' ').replace(/ accent-\w+ /g, ' ').replace(/^\s+|\s+$/g, '');
    root.className = (root.className ? root.className + ' ' : '') + 'accent-' + this.current.accent;
    useTwelveHour(this.current.clock === '12');
  }
}
