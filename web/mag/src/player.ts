// SPDX-License-Identifier: AGPL-3.0-or-later

// Playing a channel: asking the server for a link (docs/api/device.md),
// with the parental PIN when the channel is locked (D55), starting a
// stream again a few times if it stops, and changing channel within the
// list it was played from.

import { type ApiError, type Channel, type Client, describe, type PlayLink } from './api';
import { forgetPIN, keepPIN, keptPIN } from './pin';
import type { Platform, PlayerEvent } from './platform';

/** A failing stream is started again at most maxRetries times in retryWindowMs. */
const maxRetries = 3;
const retryWindowMs = 60000;

export interface PlayerHost {
  /** Asks for the PIN, saying why; done gets it, or null if the viewer gave up. */
  askPIN(c: Channel, message: string, done: (pin: string | null) => void): void;
  /** A channel started, or the player moved to another. */
  playing(c: Channel): void;
  /** Playback stopped by itself, or could not start; text says why. */
  stopped(text: string): void;
  /** The box is no longer allowed in, or the lineup changed under it. */
  lost(err: ApiError): void;
}

export class Player {
  /** The channel that plays, or null. */
  channel: Channel | null = null;
  /** The list that changing channel moves through, the place in it, and its group's key, if it is a group's. */
  list: Channel[] = [];
  index = 0;
  listKey = '';

  private platform: Platform;
  private client: Client;
  private host: PlayerHost;
  /** Counts play calls, so that the answer to a superseded one is ignored. */
  private seq = 0;
  private retries: number[] = [];

  constructor(platform: Platform, client: Client, host: PlayerHost) {
    this.platform = platform;
    this.client = client;
    this.host = host;
  }

  playing(): boolean {
    return this.channel !== null;
  }

  /** play plays channel c of list, the channels of group key, asking for the PIN if it needs one. */
  play(c: Channel, list: Channel[], key: string): void {
    this.retries = [];
    this.start(c, list, key, '');
  }

  /** zap plays the channel delta places along the list. */
  zap(delta: number): void {
    const n = this.list.length;
    if (n > 0) {
      const i = (((this.index + delta) % n) + n) % n;
      this.retries = [];
      this.start(this.list[i]!, this.list, this.listKey, '');
    }
  }

  stop(): void {
    this.seq++;
    this.channel = null;
    this.platform.stop();
  }

  /** forgetPIN drops a PIN that the page keeps. */
  forgetPIN(): void {
    forgetPIN();
  }

  onEvent(event: PlayerEvent): void {
    const self = this;
    if (!this.channel || event === 'started' || event === 'info' || event === 'subtitles') {
      return;
    }
    if (event === 'failed') {
      this.recover();
      return;
    }
    // ended: the stream ended, or the player went on past a discontinuity.
    const seq = this.seq;
    setTimeout(function () {
      if (seq === self.seq && self.channel && !self.platform.isPlaying()) {
        self.recover();
      }
    }, 1500);
  }

  private start(c: Channel, list: Channel[], key: string, pin: string): void {
    const self = this;
    if (c.locked && !pin) {
      pin = keptPIN();
      if (!pin) {
        this.host.askPIN(c, '', function (entered) {
          if (entered) {
            self.start(c, list, key, entered);
          }
        });
        return;
      }
    }
    const seq = ++this.seq;
    this.client.call<PlayLink>('POST', 'channels/' + c.id + '/play', pin ? { pin: pin } : null, function (err, link) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !link) {
        self.refused(c, list, key, err);
        return;
      }
      if (pin) {
        keepPIN(pin);
      }
      self.list = list;
      self.listKey = key;
      self.index = Math.max(0, list.indexOf(c));
      const changed = self.channel !== c;
      self.channel = c;
      self.platform.play(link.solution, link.url);
      if (changed) {
        self.host.playing(c);
      }
    });
  }

  private refused(c: Channel, list: Channel[], key: string, err: ApiError | null): void {
    const self = this;
    const code = err ? err.code : '';
    if (code === 'pin_required' || code === 'pin_wrong') {
      this.forgetPIN();
      this.host.askPIN(c, code === 'pin_wrong' ? 'That PIN is not right. Try again.' : '', function (entered) {
        if (entered) {
          self.start(c, list, key, entered);
        }
      });
      return;
    }
    if (code === 'pin_locked') {
      const wait = err && err.retryAfter ? Math.ceil(err.retryAfter / 60) : 5;
      this.halt('Too many wrong PINs. Try again in ' + wait + (wait === 1 ? ' minute.' : ' minutes.'));
      return;
    }
    if (err && (code === 'unauthorized' || code === 'blocked' || code === 'not_found')) {
      this.halt('');
      this.host.lost(err);
      return;
    }
    this.halt("Can't play " + label(c) + '. ' + describe(err));
  }

  private halt(text: string): void {
    const was = this.channel !== null;
    this.stop();
    if (was || text) {
      this.host.stopped(text);
    }
  }

  /** recover starts a stream that stopped again, a few times at most. */
  private recover(): void {
    const self = this;
    const c = this.channel;
    if (!c) {
      return;
    }
    const t = new Date().getTime();
    this.retries = this.retries.filter(function (r) {
      return t - r < retryWindowMs;
    });
    if (this.retries.length >= maxRetries) {
      this.halt(label(c) + ' stopped playing.');
      return;
    }
    this.retries.push(t);
    const seq = this.seq;
    setTimeout(function () {
      if (seq === self.seq && self.channel === c) {
        self.start(c, self.list, self.listKey, keptPIN());
      }
    }, 1000);
  }
}

export function label(c: Channel | null): string {
  return c ? c.number + ' ' + c.name : 'The channel';
}
