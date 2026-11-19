// SPDX-License-Identifier: AGPL-3.0-or-later

// Films and series (docs/api/device.md): the shelves, a part of a shelf at a
// time, and a title opened. The server reads a source's films or series the
// first time a box asks, so the first answer can say that they are still
// loading.

import type { ApiError, Callback, Client, LibraryState, ShelfPage, Title, TitleKind } from './api';
import { keptPIN } from './pin';

/** How many titles a part of a shelf holds. */
export const pageSize = 200;

/** How long a part of a shelf is kept, in milliseconds. */
const keepMs = 10 * 60 * 1000;

interface Kept {
  page: ShelfPage;
  at: number;
}

/** path returns the API's path for a kind's calls: movies or series. */
export function kindPath(kind: TitleKind): string {
  return kind === 'movie' ? 'movies' : 'series';
}

/** hiddenPoster says whether a title's picture stays hidden: it is locked, and the page keeps no PIN (D85). */
export function hiddenPoster(t: { locked?: boolean }): boolean {
  return !!t.locked && !keptPIN();
}

/** thumb returns the path of a poster's small copy, for a list's rows. */
export function thumb(poster: string | undefined): string {
  return poster ? poster.replace('/artwork/poster/', '/artwork/thumb/') : '';
}

/** LibraryModel asks for the films or the series, and keeps the parts of shelves for a while. */
export class LibraryModel {
  readonly kind: TitleKind;
  private client: Client;
  private kept: { [key: string]: Kept } = {};

  constructor(client: Client, kind: TitleKind) {
    this.client = client;
    this.kind = kind;
  }

  /** state asks whether the box can browse, and for the shelves. */
  state(done: Callback<LibraryState>): void {
    this.client.call<LibraryState>('GET', kindPath(this.kind), null, done);
  }

  /** shelf asks for a part of a shelf, from from, in an order: added or name. */
  shelf(id: string, order: string, from: number, done: Callback<ShelfPage>): void {
    const self = this;
    const key = id + '/' + order + '/' + from;
    const k = this.kept[key];
    if (k && new Date().getTime() - k.at < keepMs) {
      done(null, k.page);
      return;
    }
    const path = kindPath(this.kind) + '/shelves/' + encodeURIComponent(id) + '?order=' + order + '&from=' + from + '&count=' + pageSize;
    this.client.call<ShelfPage>('GET', path, null, function (err: ApiError | null, page: ShelfPage | null) {
      if (!err && page) {
        self.kept[key] = { page: page, at: new Date().getTime() };
      }
      done(err, page);
    });
  }

  /** open asks for a title, which the server may read from its source first. */
  open(id: number, done: Callback<Title>): void {
    this.client.call<Title>('GET', kindPath(this.kind) + '/' + id, null, done);
  }

  /** forget drops the parts of shelves it keeps, after the account's progress changed. */
  forget(): void {
    this.kept = {};
  }
}
