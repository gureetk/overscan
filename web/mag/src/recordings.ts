// SPDX-License-Identifier: AGPL-3.0-or-later

// The household's recordings to come and under way (D89), which the guide
// marks, and which the green key schedules and calls off. The server
// records them; those made play from Movies.

import type { Callback, Client } from './api';

export interface Recording {
  id: number;
  /** The lineup entry's ID, as the channel list gives it. */
  channel: number;
  title: string;
  /** The programme's start and end, in Unix seconds. */
  start: number;
  stop: number;
  state: 'scheduled' | 'recording' | 'done' | 'failed' | 'cancelled';
  error?: string;
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
  recordings: Recording[];
}

/** How long a list read from the server is kept, in milliseconds. */
const keepMs = 60 * 1000;

export class Recordings {
  /** The server records: the administrator gave recordings room. */
  on = false;
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
      self.list = data.recordings || [];
      done();
    });
  }

  /** find returns the recording of the programme on channel id that starts at start, if any. */
  find(id: number, start: number): Recording | null {
    for (let i = 0; i < this.list.length; i++) {
      const r = this.list[i]!;
      if (r.channel === id && r.start === start && (r.state === 'scheduled' || r.state === 'recording')) {
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

  /** cancel calls a recording off. */
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
