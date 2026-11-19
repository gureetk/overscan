// SPDX-License-Identifier: AGPL-3.0-or-later

// What is on: the page asks the server for the programmes of the channels
// it shows, a few hours at a time, and keeps them (docs/api/device.md).

import type { ChannelGuide, Client, Guide, Programme } from './api';
import { now, serverTime } from './time';

/** The most channels one guide call may name. */
const maxIds = 100;
/** How long a channel's programmes with descriptions are kept, in seconds. */
const detailsLife = 30 * 60;

interface Entry {
  /** By start. */
  programmes: Programme[];
  /** The window the server has answered for, in Unix seconds. */
  from: number;
  to: number;
}

interface Details {
  programmes: Programme[];
  at: number;
}

export class GuideCache {
  private client: Client;
  private data: { [id: string]: Entry } = {};
  private details: { [id: string]: Details } = {};
  private asking: { [key: string]: boolean } = {};

  constructor(client: Client) {
    this.client = client;
  }

  /** at returns the programme on channel id at time t, if the guide has one. */
  at(id: number, t: number): Programme | null {
    const e = this.data[id];
    if (!e) {
      return null;
    }
    for (let i = 0; i < e.programmes.length; i++) {
      const p = e.programmes[i]!;
      if (p.start <= t && t < p.stop) {
        return p;
      }
    }
    return null;
  }

  /** after returns the first programme on channel id that starts at t or later. */
  after(id: number, t: number): Programme | null {
    const e = this.data[id];
    if (!e) {
      return null;
    }
    for (let i = 0; i < e.programmes.length; i++) {
      if (e.programmes[i]!.start >= t) {
        return e.programmes[i]!;
      }
    }
    return null;
  }

  /** between returns channel id's programmes that end after from and start before to. */
  between(id: number, from: number, to: number): Programme[] {
    const e = this.data[id];
    if (!e) {
      return [];
    }
    return e.programmes.filter(function (p) {
      return p.stop > from && p.start < to;
    });
  }

  /** knows says whether the guide has asked about channel id over [from, to). */
  knows(id: number, from: number, to: number): boolean {
    const e = this.data[id];
    return !!e && e.from <= from && e.to >= to;
  }

  /**
   * wantNow makes sure the guide knows what is on now and for the next hour
   * on the channels, asking the server for those it does not, and calls done
   * when it has, whether or not the server answered.
   */
  wantNow(ids: number[], done: () => void): void {
    const t = now();
    const self = this;
    this.ask(
      ids.filter(function (id) {
        return !self.knows(id, t, t + 3600);
      }),
      0,
      0,
      done,
    );
  }

  /** want makes sure the guide knows the channels over [from, to), then calls done. */
  want(ids: number[], from: number, to: number, done: () => void): void {
    const self = this;
    this.ask(
      ids.filter(function (id) {
        return !self.knows(id, from, to);
      }),
      from,
      to,
      done,
    );
  }

  /** detailsOf returns channel id's programmes with descriptions, if the page has them. */
  detailsOf(id: number): Programme[] | null {
    const d = this.details[id];
    return d && now() - d.at < detailsLife ? d.programmes : null;
  }

  /** wantDetails asks for channel id's programmes with descriptions, then calls done. */
  wantDetails(id: number, done: () => void): void {
    const self = this;
    const key = 'd' + id;
    if (this.detailsOf(id) || this.asking[key]) {
      done();
      return;
    }
    this.asking[key] = true;
    this.client.call<ChannelGuide>('GET', 'channels/' + id + '/guide', null, function (err, g) {
      delete self.asking[key];
      if (!err && g) {
        self.details[id] = { programmes: g.programmes || [], at: now() };
        self.merge(id, g.programmes || [], g.from, g.to);
      }
      done();
    });
  }

  /** forget drops what the page knows, for a new lineup. */
  forget(): void {
    this.data = {};
    this.details = {};
  }

  private ask(ids: number[], from: number, to: number, done: () => void): void {
    const self = this;
    const batches: number[][] = [];
    for (let i = 0; i < ids.length; i += maxIds) {
      batches.push(ids.slice(i, i + maxIds));
    }
    let left = 0;
    const finished = function () {
      if (--left <= 0) {
        done();
      }
    };
    for (let b = 0; b < batches.length; b++) {
      const key = batches[b]!.join(',') + '@' + from + '-' + to;
      if (this.asking[key]) {
        continue;
      }
      this.asking[key] = true;
      left++;
      const batch = batches[b]!;
      let query = 'guide?ids=' + batch.join(',');
      if (from) {
        query += '&from=' + Math.floor(from) + '&to=' + Math.ceil(to);
      }
      const sent = now();
      this.client.call<Guide>('GET', query, null, function (err, g) {
        delete self.asking[key];
        if (!err && g) {
          if (!from) {
            serverTime(g.from); // the server's now, which the box's clock may not agree with
          }
          for (let i = 0; i < batch.length; i++) {
            const id = batch[i]!;
            self.merge(id, (g.channels && g.channels[id]) || [], g.from, g.to);
          }
        } else if (from === 0) {
          // Do not ask again at once for a window the server failed on.
          for (let i = 0; i < batch.length; i++) {
            self.merge(batch[i]!, [], sent, sent + 300);
          }
        }
        finished();
      });
    }
    if (left === 0) {
      done();
    }
  }

  private merge(id: number, programmes: Programme[], from: number, to: number): void {
    const e = this.data[id];
    if (!e || to < e.from || from > e.to) {
      this.data[id] = { programmes: programmes.slice(), from: from, to: to };
      return;
    }
    const seen: { [pid: string]: boolean } = {};
    const all: Programme[] = [];
    const add = function (list: Programme[]) {
      for (let i = 0; i < list.length; i++) {
        const p = list[i]!;
        if (!seen[p.id]) {
          seen[p.id] = true;
          all.push(p);
        }
      }
    };
    add(programmes); // newer answers win
    add(e.programmes);
    all.sort(function (a, b) {
      return a.start - b.start;
    });
    this.data[id] = { programmes: all, from: Math.min(from, e.from), to: Math.max(to, e.to) };
  }
}
