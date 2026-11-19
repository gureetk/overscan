// SPDX-License-Identifier: AGPL-3.0-or-later

// Playing a film or an episode: asking the server for a link (docs/api/
// device.md), starting where the account got to, pausing and seeking, and
// telling the server how far it got (D65). A file that the box cannot play
// as it is comes as a stream that ffmpeg makes of it (D68), which has no
// byte positions: the page seeks in it by asking for it from a time. A
// past programme of a channel plays from its source's archive the same way
// (D81), and asks for the parental PIN when its channel is locked (D55).

import {
  type ApiError,
  type ArchiveLink,
  type Channel,
  type Client,
  describe,
  type EpisodeInfo,
  getFrom,
  type ItemLink,
  type PlaybackReport,
  type Programme,
  type Progress,
  type StreamAudio,
  type StreamStart,
  type SubtitleInfo,
  type Title,
} from './api';
import { forgetPIN, keepPIN, keptPIN } from './pin';
import { externalSubtitles, type Platform, type PlayerEvent } from './platform';

/** How often the page tells the server how far it got, while one plays. */
const reportMs = 30 * 1000;
/** A stream that breaks off is started again where it was, at most this many times in a row. */
const maxRetries = 3;
/** After this long without a seek key, the page seeks where the keys took it. */
const seekWaitMs = 900;
/** An end this far before the file's length is a break, not the end. */
const breakSec = 90;
/** How often the page looks where it has got to, to see that it moves. */
const watchMs = 500;
/**
 * A film that has not moved this long after the box was given it is paused
 * and played again, once: on a box, some films stood still until the viewer
 * did that.
 */
const nudgeMs = 6000;
/** How long the nudge's pause lasts. */
const nudgePauseMs = 400;
/** A film that has not moved, nor been said to start, this long after the box was given it did not start. */
const startLimitMs = 40000;
/** A film that the player said started, but whose place the box does not give, counts as playing after this long. */
const eventGraceMs = 8000;
/** A film whose place stands still this long while it plays is waiting for its source. */
const stallMs = 2500;
/**
 * A past programme whose stream goes on past its end, as some archives'
 * streams do, ends this long after it.
 */
const programmeTailSec = 120;
/** A wait this long is told to the server. */
const longWaitMs = 10000;

/**
 * Where a film is: waiting for the server's link; given to the box, which
 * has not shown it moving yet; playing; or standing still while it plays,
 * as when the player waits for its source.
 */
export type FilmPhase = 'linking' | 'starting' | 'playing' | 'waiting';

function nowMs(): number {
  return new Date().getTime();
}

/** What plays: a film, an episode of a series, or a past programme of a channel. */
export interface Playing {
  /** The film or the series; for a past programme, one that stands for it. */
  title: Title;
  /** The episode, for a series. */
  episode: EpisodeInfo | null;
  /** The episode's season. */
  season: number;
  /** A past programme of a channel, which plays from its source's archive (D81). */
  programme?: PastProgramme | null;
}

/** A past programme of a channel. */
export interface PastProgramme {
  channel: Channel;
  programme: Programme;
}

/** pastProgramme returns what plays a past programme of a channel. */
export function pastProgramme(c: Channel, p: Programme): Playing {
  return {
    title: { id: 0, kind: 'movie', name: p.title, details: true, runtime: Math.round((p.stop - p.start) / 60) },
    episode: null,
    season: 0,
    programme: { channel: c, programme: p },
  };
}

export interface FilmHost {
  /** Asks for the parental PIN for what, saying why; done gets it, or null if the viewer gave up. */
  askPIN(what: string, message: string, done: (pin: string | null) => void): void;
  /** It started, or failed to; text says why it stopped, if it did. */
  stopped(text: string): void;
  /** Something went wrong that the viewer should hear of, while it plays on. */
  notice(text: string): void;
  /** It played to its end. */
  ended(p: Playing): void;
  /** The box is no longer allowed in, or the title has gone. */
  lost(err: ApiError): void;
  /** Something to show over the video changed: paused, or where a seek goes. */
  changed(): void;
}

/**
 * Subtitles the viewer can show (D69): a track the server lists, which the
 * box loads from its address, or one in the file that the box lists, by
 * its pid.
 */
export interface Subtitles {
  server: SubtitleInfo | null;
  pid: number;
  /** As the track says, such as eng; "" if not known. */
  lang: string;
}

/** name returns what plays as the screen says it: a film's or a programme's name, or a series' with S1 E2. */
export function playingName(p: Playing): string {
  if (p.programme) {
    return p.programme.programme.title;
  }
  if (!p.episode) {
    return p.title.name;
  }
  return p.title.name + ' · S' + p.season + ' E' + p.episode.number;
}

export class FilmPlayer {
  /** What plays, or null. */
  item: Playing | null = null;
  paused = false;
  /** Where the seek keys took it, in seconds, while it waits to seek; -1 if none. */
  target = -1;
  /** Where it is, which the screen shows while it starts or waits. */
  phase: FilmPhase = 'linking';

  private platform: Platform;
  private client: Client;
  private host: FilmHost;
  private seq = 0;
  private reportTimer = 0;
  private seekTimer = 0;
  private retries = 0;
  /** Where it should start, for a check once it started. */
  private startAt = 0;
  /** Where it is, in seconds, as the box said last. */
  private lastAt = 0;
  private length = 0;
  /** How the link that plays plays, once the server said. */
  private link: ItemLink | null = null;
  /** Where a stream through ffmpeg started, in seconds into the file: the box counts from there. */
  private offset = 0;
  /** The subtitles that show, or null; they are shown again each time a stream starts. */
  private shown: Subtitles | null = null;
  /** The sound track that plays, as its place in the link's audio, for a stream that carries one at a time. */
  private track = 0;

  // Watching that it moves, from each play command on.
  private watchTimer = 0;
  /** When the phase began, in milliseconds. */
  private phaseAt = 0;
  /** When the page asked the server for the link, and how long the server took. */
  private askedAt = 0;
  private linkMs = 0;
  /** When the box was given what plays. */
  private playAt = 0;
  /** Where the box said it was when the page looked last, in its own seconds; -1 before it looked. */
  private lastPos = -1;
  /** When it last moved, and whether it moved since the play command. */
  private movedAt = 0;
  private moved = false;
  /** How long after the play command the player said it started; -1 if it has not. */
  private eventMs = -1;
  private nudged = false;
  /** Where the play command started it, in seconds into the file. */
  private playedFrom = 0;

  constructor(platform: Platform, client: Client, host: FilmHost) {
    this.platform = platform;
    this.client = client;
    this.host = host;
  }

  /** play plays a film or an episode, from a time in seconds. */
  play(p: Playing, from: number): void {
    this.stop(true);
    this.item = p;
    this.retries = 0;
    this.track = 0;
    if (p.programme) {
      this.length = p.programme.programme.stop - p.programme.programme.start;
    } else {
      this.length = p.episode ? p.episode.duration || 0 : p.title.runtime ? p.title.runtime * 60 : 0;
    }
    this.start(Math.max(0, Math.floor(from)), '');
  }

  /** request returns the call that asks for what plays, with the PIN, if any. */
  private request(p: Playing, pin: string): { path: string; body: object } {
    if (p.programme) {
      const body: { start: number; pin?: string } = { start: p.programme.programme.start };
      if (pin) {
        body.pin = pin;
      }
      return { path: 'channels/' + p.programme.channel.id + '/archive', body: body };
    }
    return { path: p.episode ? 'episodes/' + p.episode.id + '/play' : 'movies/' + p.title.id + '/play', body: { restart: true } };
  }

  private start(from: number, pin: string): void {
    const self = this;
    const p = this.item;
    if (!p) {
      return;
    }
    if (p.programme && p.programme.channel.locked && !pin) {
      pin = keptPIN();
      if (!pin) {
        this.askPIN(p, from, '');
        return;
      }
    }
    const seq = ++this.seq;
    this.paused = false;
    this.target = -1;
    this.startAt = from;
    this.lastAt = from;
    this.link = null;
    this.offset = 0;
    clearTimeout(this.watchTimer);
    this.setPhase('linking');
    this.askedAt = nowMs();
    const call = this.request(p, pin);
    this.client.call<ItemLink | ArchiveLink>('POST', call.path, call.body, function (err, link) {
      if (seq !== self.seq) {
        return;
      }
      self.linkMs = nowMs() - self.askedAt;
      if (err || !link) {
        self.refused(p, from, err);
        return;
      }
      if (pin) {
        keepPIN(pin);
      }
      self.link = link;
      self.shown = self.sameSubtitles(self.shown, link);
      if (link.duration > 0) {
        self.length = link.duration;
      }
      if (link.seek === 'restart') {
        self.from(seq, from);
        return;
      }
      self.platform.play(link.solution, link.url + (from > 0 ? ' position:' + from : ''));
      self.began(from);
      self.schedule();
    });
  }

  /** askPIN asks for the parental PIN, saying why, and starts what plays with it. */
  private askPIN(p: Playing, from: number, message: string): void {
    const self = this;
    this.setPhase('linking');
    this.host.askPIN(playingName(p), message, function (entered) {
      if (self.item !== p) {
        return;
      }
      if (entered) {
        self.start(from, entered);
      } else {
        self.stop(false);
        self.host.stopped('');
      }
    });
  }

  /** refused deals with a call for a link that the server refused. */
  private refused(p: Playing, from: number, err: ApiError | null): void {
    const code = err ? err.code : '';
    if (code === 'pin_required' || code === 'pin_wrong') {
      forgetPIN();
      this.askPIN(p, from, code === 'pin_wrong' ? 'That PIN is not right. Try again.' : '');
      return;
    }
    this.item = null;
    if (code === 'pin_locked') {
      const wait = err && err.retryAfter ? Math.ceil(err.retryAfter / 60) : 5;
      this.host.stopped('Too many wrong PINs. Try again in ' + wait + (wait === 1 ? ' minute.' : ' minutes.'));
      return;
    }
    if (code === 'no_archive') {
      this.host.stopped("Can't play " + playingName(p) + '. ' + describe(err));
      return;
    }
    if (err && (code === 'unauthorized' || code === 'blocked' || code === 'not_found')) {
      this.host.lost(err);
      return;
    }
    this.host.stopped("Can't play " + playingName(p) + '. ' + describe(err));
  }

  /** converted reports whether what plays is a stream through ffmpeg, which the page seeks in. */
  private converted(): boolean {
    return this.link !== null && this.link.seek === 'restart';
  }

  /**
   * from plays a stream through ffmpeg from a time: it asks the server where
   * the stream from there starts, at the picture at or before it, and plays
   * it from there.
   */
  private from(seq: number, at: number): void {
    const self = this;
    const link = this.link;
    if (!link) {
      return;
    }
    // A past programme's stream starts at a whole minute, which can be
    // before the programme, so the page asks where even from its start.
    const programme = !!(this.item && this.item.programme);
    if ((at <= 0 && !programme) || !link.start) {
      this.playFrom(seq, at > 0 ? at : 0);
      return;
    }
    getFrom<StreamStart>(link.start + '?t=' + Math.max(0, at), function (err, s) {
      const least = programme ? -60 : 0;
      self.playFrom(seq, !err && s && typeof s.t === 'number' && s.t >= least ? s.t : Math.max(0, at));
    });
  }

  /** playFrom plays the stream through ffmpeg from a time it starts at. */
  private playFrom(seq: number, t: number): void {
    const link = this.link;
    if (seq !== this.seq || !link) {
      return;
    }
    this.offset = t;
    this.lastAt = Math.max(0, t);
    const query: string[] = [];
    if (t > 0) {
      query.push('t=' + t);
    }
    if (this.track > 0 && this.switchesTrack()) {
      query.push('a=' + this.track);
    }
    this.platform.play(link.solution, link.url + (query.length ? '?' + query.join('&') : ''));
    this.began(t);
    this.schedule();
    this.host.changed();
  }

  /** setPhase moves to a phase, and has the screen show it. */
  private setPhase(phase: FilmPhase): void {
    if (phase !== this.phase) {
      this.phase = phase;
      this.phaseAt = nowMs();
      this.host.changed();
    }
  }

  /** phaseSeconds returns how long it has been in its phase, in whole seconds. */
  phaseSeconds(): number {
    return Math.floor((nowMs() - this.phaseAt) / 1000);
  }

  /** began starts watching that what the box was just given moves. */
  private began(from: number): void {
    this.playAt = nowMs();
    this.playedFrom = from;
    this.lastPos = -1;
    this.movedAt = this.playAt;
    this.moved = false;
    this.eventMs = -1;
    this.nudged = false;
    this.phase = 'linking'; // so that setPhase tells the screen
    this.setPhase('starting');
    this.watch();
  }

  /** watch looks where it has got to every so often while it plays. */
  private watch(): void {
    const self = this;
    clearTimeout(this.watchTimer);
    const seq = this.seq;
    this.watchTimer = setTimeout(function () {
      if (seq === self.seq && self.item) {
        self.look(seq);
        if (seq === self.seq && self.item) {
          self.watch();
        }
      }
    }, watchMs);
  }

  /**
   * look sees whether it moved since the page looked last: a film that
   * starts moves once it plays, and one that stands still while it plays
   * waits for its source. One that does not start in a while is paused and
   * played again, once, then given up and started again.
   */
  private look(seq: number): void {
    const t = nowMs();
    if (this.paused || this.target >= 0 || this.phase === 'linking') {
      // It stands still by itself while paused or seeking.
      this.movedAt = t;
      this.lastPos = -1;
      return;
    }
    const pos = this.platform.position();
    const moving = this.lastPos >= 0 && pos > 0 && Math.abs(pos - this.lastPos) > 0.05;
    this.lastPos = pos;
    if (moving) {
      this.movedAt = t;
      this.lastAt = Math.max(0, this.offset + pos);
    }
    const p = this.item;
    if (p && p.programme && this.length > 0 && this.lastAt > this.length + programmeTailSec) {
      // The archive's stream goes on into what came after the programme.
      this.ended();
      return;
    }
    switch (this.phase) {
      case 'starting':
        if (moving) {
          this.moved = true;
          this.setPhase('playing');
          this.tell('began', t - this.playAt, 0);
        } else if (this.eventMs >= 0 && t - this.playAt > this.eventMs + eventGraceMs) {
          // The player said it started; the box may not say where it is.
          this.setPhase('playing');
          this.tell('began', -1, 0);
        } else if (t - this.playAt > startLimitMs) {
          this.tell('not_started', -1, 0);
          this.broke();
        } else if (!this.nudged && t - this.playAt > nudgeMs) {
          this.nudge(seq);
        }
        return;
      case 'playing':
        if (!moving && this.moved && t - this.movedAt > stallMs) {
          this.setPhase('waiting');
        }
        if (moving) {
          this.moved = true;
        }
        return;
      case 'waiting': {
        const waited = t - this.phaseAt + stallMs;
        if (moving) {
          if (waited > longWaitMs) {
            this.tell('waited', t - this.playAt, waited);
          }
          this.setPhase('playing');
        } else if (waited > startLimitMs) {
          this.tell('stalled', -1, waited);
          this.broke();
        }
        return;
      }
    }
  }

  /** nudge pauses what stands still and plays it again, as a viewer would. */
  private nudge(seq: number): void {
    const self = this;
    this.nudged = true;
    this.platform.pause();
    setTimeout(function () {
      if (seq === self.seq && self.item && !self.paused) {
        self.platform.resume();
      }
    }, nudgePauseMs);
  }

  /** tell tells the server how it started, or that it waited, for its log. */
  private tell(outcome: PlaybackReport['outcome'], movedMs: number, waitMs: number): void {
    const p = this.item;
    if (!p) {
      return;
    }
    const r: PlaybackReport = {
      kind: p.programme ? 'programme' : p.episode ? 'episode' : 'movie',
      id: p.programme ? p.programme.channel.id : p.episode ? p.episode.id : p.title.id,
      outcome: outcome,
      link_ms: Math.max(0, Math.round(this.linkMs)),
      moved_ms: Math.round(movedMs),
      event_ms: this.eventMs,
      wait_ms: Math.max(0, Math.round(waitMs)),
      nudged: this.nudged,
      converted: this.converted(),
      from: Math.max(0, Math.floor(this.playedFrom)),
      events: this.platform.rawEvents(),
    };
    this.client.call<object>('POST', 'playback', r, function () {});
  }

  /** switchesTrack reports whether what plays carries one sound track at a time, which the page picks by asking for it again. */
  switchesTrack(): boolean {
    return this.link !== null && this.link.audio_switch === 'restart' && !!this.link.audio && this.link.audio.length > 0;
  }

  /** currentTrack returns the sound track that plays, as its place in streamAudio, for such a stream. */
  currentTrack(): number {
    return this.track;
  }

  /** chooseTrack plays another sound track of such a stream, from where it is. */
  chooseTrack(i: number): void {
    if (!this.item || !this.switchesTrack() || i === this.track || i < 0 || i >= this.link!.audio!.length) {
      return;
    }
    this.track = i;
    this.restartAt(this.position());
  }

  /** restartAt asks for a stream that has no byte positions from a time, as a seek in it does. */
  private restartAt(to: number): void {
    const seq = ++this.seq;
    clearTimeout(this.reportTimer);
    clearTimeout(this.watchTimer);
    this.target = -1;
    this.lastAt = to;
    this.paused = false;
    this.platform.stop();
    this.setPhase('linking');
    this.from(seq, to);
    this.host.changed();
  }

  /** stop stops what plays, telling the server how far it got if save says so. */
  stop(save: boolean): void {
    if (!this.item) {
      return;
    }
    if (save) {
      this.report(false);
    }
    this.seq++;
    clearTimeout(this.reportTimer);
    clearTimeout(this.seekTimer);
    clearTimeout(this.watchTimer);
    this.item = null;
    this.link = null;
    this.paused = false;
    this.target = -1;
    this.phase = 'linking';
    this.platform.stop();
  }

  /** position returns where it is, in seconds, or where the seek keys took it. */
  position(): number {
    if (this.target >= 0) {
      return this.target;
    }
    const at = this.platform.position();
    if (at > 0) {
      this.lastAt = Math.max(0, this.offset + at);
    }
    return this.lastAt;
  }

  /** lastKnown returns where it is, in seconds, as the box said last, whatever the seek keys do. */
  lastKnown(): number {
    return this.lastAt;
  }

  /** duration returns how long it is, in seconds, 0 if not known. */
  duration(): number {
    // A stream through ffmpeg is as long as what is left from where it
    // started, as the box sees it: the server's length counts.
    const d = this.converted() ? 0 : this.platform.duration();
    if (d > 0) {
      this.length = d;
    }
    return this.length;
  }

  /** streamAudio returns the sound tracks of a stream through ffmpeg, in the box's order, or null. */
  streamAudio(): StreamAudio[] | null {
    return this.converted() && this.link && this.link.audio ? this.link.audio : null;
  }

  /**
   * subtitles returns the subtitles the viewer can show: the server's, then,
   * for a file the box plays as it is, those in it that the box lists.
   */
  subtitles(): Subtitles[] {
    const out: Subtitles[] = [];
    const link = this.link;
    if (link && link.subtitles) {
      for (let i = 0; i < link.subtitles.length; i++) {
        const s = link.subtitles[i]!;
        out.push({ server: s, pid: 0, lang: s.language || '' });
      }
    }
    if (link && !this.converted()) {
      const own = this.platform.subtitleTracks();
      for (let i = 0; i < own.length; i++) {
        if (own[i]!.pid !== externalSubtitles) {
          out.push({ server: null, pid: own[i]!.pid, lang: own[i]!.lang });
        }
      }
    }
    return out;
  }

  /** shownSubtitles returns the subtitles that show, or null. */
  shownSubtitles(): Subtitles | null {
    return this.shown;
  }

  /** showSubtitles shows subtitles, or none with null. */
  showSubtitles(s: Subtitles | null): void {
    this.shown = s;
    if (this.item) {
      this.applySubtitles();
    }
  }

  /** applySubtitles has the box show what the viewer chose, which a new stream forgets. */
  private applySubtitles(): void {
    const s = this.shown;
    if (!s) {
      this.platform.showSubtitles(0);
    } else if (s.server) {
      // A stream through ffmpeg starts part way through: the subtitles come
      // moved by as much.
      this.platform.loadSubtitles(s.server.url + (this.converted() && this.offset > 0 ? '?t=' + this.offset : ''));
    } else {
      this.platform.showSubtitles(s.pid);
    }
  }

  /**
   * sameSubtitles returns the subtitles of a new link that match those that
   * showed, as for the next episode: of the same language and kind, from
   * the server; or null.
   */
  private sameSubtitles(was: Subtitles | null, link: ItemLink): Subtitles | null {
    if (!was || !was.server || !link.subtitles) {
      return null;
    }
    for (let i = 0; i < link.subtitles.length; i++) {
      const s = link.subtitles[i]!;
      if ((s.language || '') === was.lang && !!s.forced === !!was.server.forced && !!s.hearing === !!was.server.hearing) {
        return { server: s, pid: 0, lang: s.language || '' };
      }
    }
    return null;
  }

  togglePause(): void {
    if (!this.item) {
      return;
    }
    if (this.paused) {
      this.platform.resume();
      this.paused = false;
      this.movedAt = nowMs();
      this.lastPos = -1;
      if (this.phase === 'starting') {
        // The viewer paused it and played it again: the nudge, done by
        // hand. It has a while again to start.
        this.playAt = nowMs();
        this.nudged = true;
      }
      this.schedule();
    } else {
      this.platform.pause();
      this.paused = true;
      this.report(false);
      clearTimeout(this.reportTimer);
    }
    this.host.changed();
  }

  /**
   * seekBy moves where the seek keys take it by delta seconds; once the keys
   * stop for a moment, it seeks there, so that a run of presses opens one
   * new connection to the source rather than one each.
   */
  seekBy(delta: number): void {
    const self = this;
    if (!this.item) {
      return;
    }
    const from = this.target >= 0 ? this.target : this.position();
    const end = this.duration();
    this.target = Math.max(0, end > 0 ? Math.min(end - 5, from + delta) : from + delta);
    clearTimeout(this.seekTimer);
    this.seekTimer = setTimeout(function () {
      self.seekNow();
    }, seekWaitMs);
    this.host.changed();
  }

  /** seekNow seeks where the seek keys took it, without waiting for the keys to stop. */
  seekNow(): void {
    clearTimeout(this.seekTimer);
    if (!this.item || this.target < 0) {
      return;
    }
    const to = this.target;
    if (this.converted()) {
      // The stream has no byte positions: ask for it from there.
      this.restartAt(to);
      return;
    }
    this.platform.seek(to);
    this.lastAt = to;
    this.target = -1;
    if (this.paused) {
      this.platform.resume();
      this.paused = false;
    }
    // It stands still while the player reads from the new place.
    this.lastPos = -1;
    this.movedAt = nowMs();
    if (this.phase === 'playing') {
      this.setPhase('waiting');
    }
    this.schedule();
    this.host.changed();
  }

  onEvent(event: PlayerEvent): void {
    const self = this;
    if (!this.item) {
      return;
    }
    switch (event) {
      case 'started': {
        if (this.eventMs < 0 && this.playAt > 0) {
          this.eventMs = nowMs() - this.playAt;
        }
        // A box that did not take position: in the play command starts at
        // the beginning: move it where it should be. A stream through
        // ffmpeg starts where it should.
        const want = this.startAt;
        const seq = this.seq;
        if (want > 30 && !this.converted()) {
          setTimeout(function () {
            if (seq === self.seq && self.platform.position() < want - 30) {
              self.platform.seek(want);
            }
          }, 1500);
        }
        if (this.shown) {
          this.applySubtitles();
        }
        this.host.changed();
        return;
      }
      case 'subtitles':
        this.host.notice("The subtitles couldn't be loaded.");
        return;
      case 'failed':
        this.broke();
        return;
      case 'ended': {
        const seq = this.seq;
        setTimeout(function () {
          if (seq === self.seq && self.item && !self.platform.isPlaying()) {
            self.ended();
          }
        }, 1000);
        return;
      }
    }
  }

  /** ended tells the end of the file from a break in it. */
  private ended(): void {
    const p = this.item;
    const length = this.duration();
    if (!p) {
      return;
    }
    if (length > 0 && this.lastAt < length - breakSec) {
      this.broke();
      return;
    }
    this.report(true);
    this.seq++;
    clearTimeout(this.reportTimer);
    clearTimeout(this.watchTimer);
    this.item = null;
    this.platform.stop();
    this.host.ended(p);
  }

  /** broke starts a stream that broke off again where it was, a few times at most. */
  private broke(): void {
    const self = this;
    const p = this.item;
    if (!p) {
      return;
    }
    if (this.retries >= maxRetries) {
      this.stop(true);
      this.host.stopped(playingName(p) + ' stopped playing.');
      return;
    }
    this.retries++;
    const at = Math.max(0, Math.floor(this.lastAt) - 5);
    const seq = ++this.seq;
    clearTimeout(this.watchTimer);
    this.platform.stop();
    this.setPhase('linking');
    setTimeout(function () {
      if (seq === self.seq && self.item === p) {
        self.start(at, keptPIN());
      }
    }, 1500);
  }

  /** schedule tells the server how far it got every so often while it plays. */
  private schedule(): void {
    const self = this;
    clearTimeout(this.reportTimer);
    const seq = this.seq;
    this.reportTimer = setTimeout(function () {
      if (seq === self.seq && self.item && !self.paused) {
        self.report(false);
        if (self.lastAt > self.startAt + 60) {
          self.retries = 0; // it plays on well past where it broke off
        }
        self.schedule();
      }
    }, reportMs);
  }

  /** report tells the server how far it got into a film or an episode, or that it finished. */
  private report(finished: boolean): void {
    const p = this.item;
    if (!p || p.programme) {
      return;
    }
    const path = p.episode ? 'episodes/' + p.episode.id + '/progress' : 'movies/' + p.title.id + '/progress';
    const body = { position: Math.floor(this.position()), duration: Math.floor(this.duration()), finished: finished };
    const progress: Progress = { position: body.position, duration: body.duration, finished: finished, watched_at: 0 };
    if (p.episode) {
      p.episode.progress = progress;
    } else {
      p.title.progress = progress;
    }
    this.client.call<Progress>('PUT', path, body, function () {});
  }
}
