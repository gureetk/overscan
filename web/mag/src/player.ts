// SPDX-License-Identifier: AGPL-3.0-or-later

// Playing a channel: asking the server for a link (docs/api/device.md),
// with the parental PIN when the channel is locked (D55), starting a
// stream again a few times if it stops, and changing channel within the
// list it was played from. While the server keeps the channel in a
// timeshift buffer (D92), it pauses, and plays from a while before live.

import { type ApiError, type Channel, type Client, describe, type PlayLink, touch } from './api';
import { forgetPIN, keepPIN, keptPIN } from './pin';
import type { Platform, PlayerEvent } from './platform';

/** A failing stream is started again at most maxRetries times in retryWindowMs. */
const maxRetries = 3;
const retryWindowMs = 60000;

/** After this long without a seek key, the page plays from where the keys took it. */
const seekWaitMs = 900;
/** While paused, the page tells the server to keep the buffer this often. */
const holdEveryMs = 15000;
/** Nearer live than this is live. */
const liveMs = 3000;
/** The page stays this far from the oldest part of the buffer, which the server drops as it goes. */
const marginMs = 10000;

function nowMs(): number {
  return new Date().getTime();
}

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

  /** The link that plays, which says whether it goes through a timeshift buffer. */
  private link: PlayLink | null = null;
  /** How far behind live what plays is, in ms, while it plays. */
  private behindMs = 0;
  /** When it was paused, by the page's clock; 0 while it plays. */
  private pausedAt = 0;
  /** Where the seek keys took it, in ms behind live, while they wait; -1 if none. */
  private target = -1;
  private seekTimer = 0;
  private holdTimer = 0;
  /** It was played from the buffer again once after it stopped. */
  private shiftRetried = false;

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
    this.release();
    this.platform.stop();
  }

  /** shiftable reports whether the channel plays through a timeshift buffer, so that it can pause and go back (D92). */
  shiftable(): boolean {
    return this.channel !== null && this.link !== null && (this.link.timeshift || 0) > 0;
  }

  /** paused reports whether the channel is paused. */
  paused(): boolean {
    return this.pausedAt > 0;
  }

  /** behind returns how far behind live what shows is, in seconds, or where the seek keys took it. */
  behind(): number {
    if (this.target >= 0) {
      return this.target / 1000;
    }
    let ms = this.behindMs;
    if (this.pausedAt > 0) {
      ms += nowMs() - this.pausedAt;
    }
    return Math.min(ms, this.keptMs()) / 1000;
  }

  /** shifted reports whether the channel is paused, or plays from before live. */
  shifted(): boolean {
    return this.shiftable() && (this.pausedAt > 0 || this.behind() > 0);
  }

  /** pauseToggle pauses the channel, or plays it on from where it was paused. */
  pauseToggle(): void {
    if (!this.shiftable()) {
      return;
    }
    if (this.pausedAt > 0) {
      this.behindMs = Math.min(this.behindMs + nowMs() - this.pausedAt, this.keptMs());
      this.pausedAt = 0;
      this.stopHolding();
      this.playShifted();
      return;
    }
    if (this.target >= 0) {
      this.behindMs = this.target < liveMs ? 0 : this.target;
      this.target = -1;
      clearTimeout(this.seekTimer);
    }
    this.platform.pause();
    this.pausedAt = nowMs();
    this.startHolding();
  }

  /**
   * seekShift moves delta seconds along what the server keeps, towards live
   * for more than 0; once the keys stop for a moment, it plays from there,
   * so that a run of presses asks the server once.
   */
  seekShift(delta: number): void {
    const self = this;
    if (!this.shiftable()) {
      return;
    }
    const from = this.target >= 0 ? this.target : this.behind() * 1000;
    this.target = Math.max(0, Math.min(this.keptMs(), from - delta * 1000));
    clearTimeout(this.seekTimer);
    this.seekTimer = setTimeout(function () {
      self.seekNow();
    }, seekWaitMs);
  }

  /** live plays the channel live again. */
  live(): void {
    if (this.shiftable()) {
      this.target = 0;
      this.seekNow();
    }
  }

  private seekNow(): void {
    clearTimeout(this.seekTimer);
    if (this.target < 0) {
      return;
    }
    this.behindMs = this.target < liveMs ? 0 : this.target;
    this.target = -1;
    this.pausedAt = 0;
    this.shiftRetried = false;
    this.stopHolding();
    this.playShifted();
  }

  /** keptMs returns how far back the page goes in the buffer. */
  private keptMs(): number {
    const kept = this.link && this.link.timeshift ? this.link.timeshift * 1000 : 0;
    return Math.max(0, kept - marginMs);
  }

  /** playShifted plays the link from behindMs before live. */
  private playShifted(): void {
    const link = this.link;
    if (!link) {
      return;
    }
    const s = Math.round(this.behindMs / 1000);
    this.platform.play(link.solution, s > 0 ? link.url + '?behind=' + s : link.url);
  }

  private startHolding(): void {
    const link = this.link;
    this.stopHolding();
    if (link) {
      this.holdTimer = setInterval(function () {
        touch(link.url + '?hold=1');
      }, holdEveryMs);
    }
  }

  private stopHolding(): void {
    clearInterval(this.holdTimer);
    this.holdTimer = 0;
  }

  /** release lets the server end the timeshift buffer of the link that played, and forgets it. */
  private release(): void {
    const link = this.link;
    if (link && link.timeshift) {
      touch(link.url + '?stop=1');
    }
    clearTimeout(this.seekTimer);
    this.stopHolding();
    this.link = null;
    this.behindMs = 0;
    this.pausedAt = 0;
    this.target = -1;
    this.shiftRetried = false;
  }

  /** forgetPIN drops a PIN that the page keeps. */
  forgetPIN(): void {
    forgetPIN();
  }

  onEvent(event: PlayerEvent): void {
    const self = this;
    // A paused channel plays on from the buffer when the viewer says.
    if (!this.channel || this.pausedAt > 0 || event === 'started' || event === 'info' || event === 'subtitles') {
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
      self.release();
      self.link = link;
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
    // Behind live, it plays on from the buffer once; then, or if that
    // fails, the channel starts again live.
    if (this.shiftable() && this.behindMs > 0 && !this.shiftRetried) {
      this.shiftRetried = true;
      const seq = this.seq;
      setTimeout(function () {
        if (seq === self.seq && self.channel === c && self.pausedAt === 0) {
          self.playShifted();
        }
      }, 1000);
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
