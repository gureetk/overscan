// SPDX-License-Identifier: AGPL-3.0-or-later

// The household's recordings to come and under way (D89, D98), which the
// guide, Live TV and full screen mark, and which the green key or REC
// starts, stops or calls off; and those of the last day that ended, which
// the Recordings screen lists with them (D100). The server records them;
// those made play from Movies.

import type { Callback, Client } from './api';

export interface Recording {
  id: number;
  /** The lineup entry's ID, as the channel list gives it. */
  channel: number;
  title: string;
  /**
   * The programme's start and end, in Unix seconds; for a channel recorded
   * from now on, when it began, and when it ends at the latest.
   */
  start: number;
  stop: number;
  /** Set for a recording that runs until it is stopped (D98). */
  until_stopped?: boolean;
  state: 'scheduled' | 'recording' | 'done' | 'failed' | 'cancelled';
  error?: string;
  /** The bytes of its file, for one that is done or failed after it began (D100). */
  size?: number;
}

/** active says whether a recording is to come or under way. */
function active(r: Recording): boolean {
  return r.state === 'scheduled' || r.state === 'recording';
}

/**
 * recordingNow says whether a recording is under way at t: the server
 * begins one a minute before its programme, and looks for those due every
 * few seconds.
 */
export function recordingNow(r: Recording, t: number): boolean {
  return r.state === 'recording' || r.start - 60 <= t;
}

interface RecordingList {
  on: boolean;
  /** How a recording started on a channel ends on a box that keeps the household's setting (D99). */
  until?: 'programme' | 'stopped';
  /** The most hours that a recording which runs until it is stopped may run. */
  max_hours?: number;
  recordings: Recording[];
  /** The household's recordings of the last day that are done or failed, the latest first (D100). */
  recent?: Recording[];
}

/** How long a list read from the server is kept, in milliseconds. */
const keepMs = 60 * 1000;

export class Recordings {
  /** The server records: the administrator gave recordings room. */
  on = false;
  /** How a recording started on a channel ends for a box that keeps the household's setting (D99). */
  until: 'programme' | 'stopped' = 'programme';
  /** The most hours that a recording which runs until it is stopped may run. */
  maxHours = 4;
  /** The household's recordings of the last day that are done or failed, the latest first (D100). */
  recent: Recording[] = [];
  private client: Client;
  private list: Recording[] = [];
  private readAt = 0;
  private asking = false;

  constructor(client: Client) {
    this.client = client;
  }

  /** load reads the list again, unless it is fresh, and calls done once it changed. */
  load(done: () => void, force?: boolean): void {
    const self = this;
    const t = new Date().getTime();
    if (this.asking || (!force && t - this.readAt < keepMs)) {
      return;
    }
    this.asking = true;
    this.client.call<RecordingList>('GET', 'recordings', null, function (err, data) {
      self.asking = false;
      if (err || !data) {
        return;
      }
      self.readAt = new Date().getTime();
      self.on = !!data.on;
      self.until = data.until === 'stopped' ? 'stopped' : 'programme';
      self.maxHours = data.max_hours || 4;
      self.list = data.recordings || [];
      self.recent = data.recent || [];
      done();
    });
  }

  /**
   * forProgramme returns the recording of a programme on channel id, from
   * start to stop: one of the programme from the guide, else one of the
   * channel that runs over it.
   */
  forProgramme(id: number, start: number, stop: number): Recording | null {
    let over: Recording | null = null;
    for (let i = 0; i < this.list.length; i++) {
      const r = this.list[i]!;
      if (r.channel !== id || !active(r)) {
        continue;
      }
      if (r.start === start) {
        return r;
      }
      if (!over && r.start < stop && r.stop > start) {
        over = r;
      }
    }
    return over;
  }

  /** underWay returns the recordings under way at t, the earliest first. */
  underWay(t: number): Recording[] {
    return this.list.filter(function (r) {
      return active(r) && recordingNow(r, t);
    });
  }

  /** toCome returns the recordings that have not begun at t, the soonest first. */
  toCome(t: number): Recording[] {
    return this.list.filter(function (r) {
      return active(r) && !recordingNow(r, t);
    });
  }

  /** onChannel returns the recording of channel id that is under way at t, if any. */
  onChannel(id: number, t: number): Recording | null {
    for (let i = 0; i < this.list.length; i++) {
      const r = this.list[i]!;
      if (r.channel === id && active(r) && recordingNow(r, t) && t < r.stop + (r.until_stopped ? 0 : 120)) {
        return r;
      }
    }
    return null;
  }

  /** record schedules the programme on channel id that starts at start. */
  record(id: number, start: number, pin: string, done: Callback<Recording>): void {
    const self = this;
    const body: { channel: number; start: number; pin?: string } = { channel: id, start: start };
    if (pin) {
      body.pin = pin;
    }
    this.client.call<Recording>('POST', 'recordings', body, function (err, r) {
      if (r) {
        self.put(r);
      } else if (err && err.code === 'recording_off') {
        self.on = false;
      }
      done(err, r);
    });
  }

  /** recordNow records channel id from now on (D98). */
  recordNow(id: number, pin: string, done: Callback<Recording>): void {
    const self = this;
    const body: { channel: number; now: boolean; pin?: string } = { channel: id, now: true };
    if (pin) {
      body.pin = pin;
    }
    this.client.call<Recording>('POST', 'recordings', body, function (err, r) {
      if (r) {
        self.put(r);
      } else if (err && err.code === 'recording_off') {
        self.on = false;
      }
      done(err, r);
    });
  }

  /** cancel calls a recording to come off, or stops one under way, which keeps what it recorded. */
  cancel(r: Recording, done: Callback<Recording>): void {
    const self = this;
    this.client.call<Recording>('DELETE', 'recordings/' + r.id, null, function (err, got) {
      if (!err) {
        self.drop(r.id);
      }
      done(err, got);
    });
  }

  private put(r: Recording): void {
    this.drop(r.id);
    this.list.push(r);
  }

  private drop(id: number): void {
    const kept: Recording[] = [];
    for (let i = 0; i < this.list.length; i++) {
      if (this.list[i]!.id !== id) {
        kept.push(this.list[i]!);
      }
    }
    this.list = kept;
  }
}
