// SPDX-License-Identifier: AGPL-3.0-or-later

// The playback check (D70): the box plays the clips that the server made,
// one after the other, and the viewer says whether each plays. A clip that
// the box does not start, or cannot open, does not play. The server keeps
// what the check found, and plays films by it.
//
// The page asks as soon as it gives the box a clip: the viewer sees whether
// it plays, whatever the player says. A clip has started once the player
// says so, the box's place in it moves, or the viewer says it plays.

import { type ApiError, type CheckClip, type CheckResult, type CheckSaved, type CheckState, type Client, describe, shutOut } from './api';
import type { Platform, PlayerEvent } from './platform';

/** How long a clip may take to start, with no answer, before it is tried again, or does not play. */
const startMs = 20000;
/** How often the page looks where the box is in a clip that starts, to see it move. */
const watchMs = 500;
/** How long after a clip starts the page reads what the box's player says of it. */
const settleMs = 2000;
/** How often the page asks whether the clips are made. */
const pollMs = 2000;
/** How long the page says that a clip did not start before it goes on. */
const skipMs = 1800;
/** A clip that ends while the viewer has not answered plays again, at most this often. */
const maxReplays = 3;
/** The most of what the box's player says that the page sends. */
const maxInfo = 250;
/** The most of the player's events, as the box gave them, that the page sends for a clip. */
const maxEvents = 120;

/**
 * Where the check is: not started; the server making the clips; the server
 * without them; playing them; sending what it found; done; stopped at the
 * first clip, which every box must play; or unable to send what it found.
 */
export type CheckPhase = 'idle' | 'preparing' | 'unavailable' | 'running' | 'saving' | 'done' | 'stopped' | 'failed';

/** Where a clip is: starting, playing while the page asks, or not started. */
export type ClipPhase = 'starting' | 'asking' | 'skipping';

export interface CheckHost {
  /** Something to show changed. */
  changed(): void;
  /** The box is no longer allowed in. */
  lost(err: ApiError): void;
}

export class PlaybackCheck {
  phase: CheckPhase = 'idle';
  clipPhase: ClipPhase = 'starting';
  /** When the box was last checked, in Unix seconds; 0 if never, -1 if not known yet. */
  checked = -1;
  /** How many clips the server has made, of how many, while it makes them. */
  ready = 0;
  total = 0;
  clips: CheckClip[] = [];
  /** The clip that plays, as an index of clips. */
  index = 0;
  results: CheckResult[] = [];
  saved: CheckSaved | null = null;
  /** Why the check could not go on, for the screen. */
  error = '';

  private platform: Platform;
  private client: Client;
  private host: CheckHost;
  /** Counts what the check started, so that late answers and timers are ignored. */
  private seq = 0;
  private timer = 0;
  private settleTimer = 0;
  private watchTimer = 0;
  /** Where the box said it was in the clip when the page looked last; -1 before it looked. */
  private lastPos = -1;
  private startedAt = 0;
  private tries = 0;
  private replays = 0;
  private current: CheckResult | null = null;

  constructor(platform: Platform, client: Client, host: CheckHost) {
    this.platform = platform;
    this.client = client;
    this.host = host;
  }

  /** active says whether the check has the player: events go to it. */
  active(): boolean {
    return this.phase === 'running';
  }

  /** playing says whether a clip plays, for the video window. */
  playing(): boolean {
    return this.phase === 'running' && this.clipPhase !== 'skipping';
  }

  /** load asks the server when the box was last checked, which also has it make the clips. */
  load(): void {
    const self = this;
    if (this.phase !== 'idle') {
      return;
    }
    this.client.call<CheckState>('GET', 'check', null, function (err, c) {
      if (!err && c) {
        self.checked = c.checked;
        self.host.changed();
      }
    });
  }

  /** start starts the check, once the server has made the clips. */
  start(): void {
    this.stop();
    const seq = ++this.seq;
    this.phase = 'preparing';
    this.ready = 0;
    this.total = 0;
    this.error = '';
    this.saved = null;
    this.host.changed();
    this.poll(seq);
  }

  private poll(seq: number): void {
    const self = this;
    this.client.call<CheckState>('GET', 'check', null, function (err, c) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !c) {
        if (err && shutOut(err)) {
          self.phase = 'idle';
          self.host.lost(err);
          return;
        }
        self.phase = 'failed';
        self.error = "Couldn't ask the server for the clips. " + describe(err);
        self.host.changed();
        return;
      }
      self.checked = c.checked;
      self.ready = c.ready;
      self.total = c.total;
      if (c.state === 'unavailable' || (c.state === 'ready' && !(c.clips && c.clips.length))) {
        self.phase = 'unavailable';
      } else if (c.state === 'preparing') {
        self.timer = setTimeout(function () {
          self.poll(seq);
        }, pollMs);
      } else {
        self.clips = c.clips || [];
        self.results = [];
        self.index = 0;
        self.phase = 'running';
        self.play(seq, false);
      }
      self.host.changed();
    });
  }

  /** play plays the clip at index; again says that it is played once more. */
  private play(seq: number, again: boolean): void {
    const self = this;
    const clip = this.clips[this.index];
    if (!clip || seq !== this.seq) {
      return;
    }
    if (!again) {
      this.tries = 0;
      this.replays = 0;
      this.current = {
        id: clip.id,
        answer: '',
        started: false,
        failed: false,
        ms: 0,
        width: 0,
        height: 0,
        tracks: 0,
        position: 0,
        video: '',
        audio: '',
        events: '',
      };
      this.clipPhase = 'starting';
    }
    this.tries++;
    this.startedAt = new Date().getTime();
    clearTimeout(this.timer);
    clearTimeout(this.settleTimer);
    this.platform.play(clip.solution, clip.url);
    this.lastPos = -1;
    this.watch(seq);
    this.timer = setTimeout(function () {
      if (seq === self.seq && self.clipPhase === 'starting') {
        self.notStarted(seq, false);
      }
    }, startMs);
    this.host.changed();
  }

  /** watch looks where the box is in a clip that starts, every so often, to see it move. */
  private watch(seq: number): void {
    const self = this;
    clearTimeout(this.watchTimer);
    this.watchTimer = setTimeout(function () {
      if (seq !== self.seq || self.phase !== 'running' || self.clipPhase !== 'starting') {
        return;
      }
      const pos = self.platform.position();
      if (self.lastPos >= 0 && pos > 0 && pos !== self.lastPos) {
        self.began(seq);
        return;
      }
      self.lastPos = pos;
      self.watch(seq);
    }, watchMs);
  }

  /** began takes a clip as started: the player said so, or the box moved in it. */
  private began(seq: number): void {
    const self = this;
    const r = this.current;
    if (!r || this.clipPhase !== 'starting') {
      return;
    }
    clearTimeout(this.timer);
    clearTimeout(this.watchTimer);
    r.started = true;
    r.ms = new Date().getTime() - this.startedAt;
    this.clipPhase = 'asking';
    clearTimeout(this.settleTimer);
    this.settleTimer = setTimeout(function () {
      if (seq === self.seq) {
        self.readPlayer();
      }
    }, settleMs);
    this.host.changed();
  }

  /** onEvent takes the player's events while the check plays. */
  onEvent(event: PlayerEvent): void {
    const seq = this.seq;
    const r = this.current;
    if (this.phase !== 'running' || !r) {
      return;
    }
    const self = this;
    switch (event) {
      case 'started':
        this.began(seq);
        return;
      case 'info':
        if (this.clipPhase === 'asking') {
          this.readPlayer();
        }
        return;
      case 'failed':
        if (this.clipPhase === 'starting') {
          clearTimeout(this.timer);
          this.notStarted(seq, true);
        } else {
          r.failed = true;
        }
        return;
      case 'ended':
        // A clip that ends before the viewer answers plays again.
        if (this.clipPhase === 'asking' && this.replays < maxReplays) {
          this.replays++;
          this.timer = setTimeout(function () {
            if (seq === self.seq && self.clipPhase === 'asking' && !self.platform.isPlaying()) {
              self.platform.play(self.clips[self.index]!.solution, self.clips[self.index]!.url);
            }
          }, 500);
        }
        return;
    }
  }

  /** notStarted tries a clip once more, and then goes on without it. */
  private notStarted(seq: number, failed: boolean): void {
    const self = this;
    const r = this.current;
    if (!r) {
      return;
    }
    clearTimeout(this.watchTimer);
    r.events = clean(this.platform.rawEvents()).substring(0, maxEvents);
    this.platform.stop();
    if (this.tries < 2) {
      this.play(seq, true);
      return;
    }
    r.failed = failed;
    r.ms = new Date().getTime() - this.startedAt;
    this.clipPhase = 'skipping';
    this.host.changed();
    this.timer = setTimeout(function () {
      if (seq === self.seq) {
        self.next(seq);
      }
    }, skipMs);
  }

  /** readPlayer keeps what the box's player says of the clip that plays. */
  private readPlayer(): void {
    const r = this.current;
    if (!r) {
      return;
    }
    const v = this.platform.videoInfo();
    r.width = Math.max(0, Math.round(v.width));
    r.height = Math.max(0, Math.round(v.height));
    r.tracks = this.platform.audioTracks().length;
    const info = this.platform.playerInfo();
    r.video = clean(info.video);
    r.audio = clean(info.audio);
  }

  /**
   * answer takes the viewer's answer to the clip that plays: yes, no, or ""
   * for not sure. The viewer may answer while the clip starts, as far as
   * the page knows: one they see playing has started.
   */
  answer(a: '' | 'yes' | 'no'): void {
    const r = this.current;
    if (this.phase !== 'running' || (this.clipPhase !== 'asking' && this.clipPhase !== 'starting') || !r) {
      return;
    }
    if (this.clipPhase === 'starting' && a === 'yes') {
      r.started = true;
      r.ms = new Date().getTime() - this.startedAt;
    }
    if (!r.width && !r.video) {
      this.readPlayer();
    }
    r.position = Math.max(0, Math.round(this.platform.position() * 10) / 10);
    r.answer = a;
    this.next(this.seq);
  }

  /** replay plays the clip again from its start, while the page asks. */
  replay(): void {
    if (this.phase === 'running' && (this.clipPhase === 'asking' || this.clipPhase === 'starting')) {
      this.replays = 0;
      this.platform.play(this.clips[this.index]!.solution, this.clips[this.index]!.url);
    }
  }

  /** next keeps what was found of the clip, and plays the next, or sends what the check found. */
  private next(seq: number): void {
    const r = this.current;
    clearTimeout(this.timer);
    clearTimeout(this.settleTimer);
    clearTimeout(this.watchTimer);
    if (r && !r.events) {
      r.events = clean(this.platform.rawEvents()).substring(0, maxEvents);
    }
    this.platform.stop();
    if (!r) {
      return;
    }
    this.results.push(r);
    this.current = null;
    // Every box plays the first clip; one that does not, here, cannot be
    // checked as it is set up.
    if (this.index === 0 && r.answer !== 'yes') {
      this.phase = 'stopped';
      this.host.changed();
      return;
    }
    this.index++;
    if (this.index < this.clips.length) {
      this.play(seq, false);
      return;
    }
    this.save();
  }

  /** save sends what the check found; once more after it failed. */
  save(): void {
    const self = this;
    const seq = ++this.seq;
    this.phase = 'saving';
    this.error = '';
    this.host.changed();
    this.client.call<CheckSaved>('PUT', 'check', { firmware: this.platform.firmware, results: this.results }, function (err, saved) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !saved) {
        if (err && shutOut(err)) {
          self.phase = 'idle';
          self.host.lost(err);
          return;
        }
        self.phase = 'failed';
        self.error = "Couldn't send what the check found. " + describe(err);
        self.host.changed();
        return;
      }
      self.saved = saved;
      self.checked = saved.checked;
      self.phase = 'done';
      self.host.changed();
    });
  }

  /** retry sends what the check found again, if that failed, or else starts it again. */
  retry(): void {
    if (this.clips.length > 0 && this.results.length === this.clips.length) {
      this.save();
    } else {
      this.start();
    }
  }

  /** stop stops the check, and what plays; nothing is sent. */
  stop(): void {
    const playing = this.phase === 'running';
    this.seq++;
    clearTimeout(this.timer);
    clearTimeout(this.settleTimer);
    clearTimeout(this.watchTimer);
    this.current = null;
    this.phase = 'idle';
    if (playing) {
      this.platform.stop();
    }
  }

  /** answered counts the clips that the viewer said play, and those that do not. */
  answered(): { plays: number; not: number } {
    let plays = 0;
    let not = 0;
    for (let i = 0; i < this.results.length; i++) {
      const r = this.results[i]!;
      if (r.answer === 'yes') {
        plays++;
      } else if (r.answer === 'no' || r.failed || !r.started) {
        not++;
      }
    }
    return { plays: plays, not: not };
  }
}

/** clean returns what the box's player said, in printable ASCII, cut short. */
function clean(text: string): string {
  return String(text || '')
    .replace(/[^\x20-\x7e]/g, '?')
    .substring(0, maxInfo);
}
