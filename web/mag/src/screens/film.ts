// SPDX-License-Identifier: AGPL-3.0-or-later

// Watching a film, an episode or a past programme full screen, with a
// panel over the video when it is paused, when the remote seeks, while it
// starts or waits for its source, or when asked: what plays, how far it
// is, and how long it lasts.
// OK pauses and goes on; Left and Right seek ten seconds back and thirty
// on, Page Up and Down (and the channel keys) five minutes; the page seeks
// once the keys stop for a moment.

import { el, setHTML, setText, setWidth, show } from '../dom';
import type { FilmPhase, Playing, Subtitles } from '../film';
import { icon } from '../icons';
import type { Press } from '../keys';
import { label } from '../player';
import { day, hms, span } from '../time';
import { chooseAudio, language, trackItems } from './watch';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';

/** How long the panel shows by itself. */
const panelMs = 5000;
/** How often the panel's times move while it shows. */
const drawMs = 1000;

export class FilmScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private panel: HTMLElement;
  private stateIcon: HTMLElement;
  private name: HTMLElement;
  private status: HTMLElement;
  private sub: HTMLElement;
  private barBox: HTMLElement;
  private bar: HTMLElement;
  private marker: HTMLElement;
  private at: HTMLElement;
  private left: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private hideTimer = 0;
  private drawTimer = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen film', parent);
    this.panel = el('div', 'banner film-panel', this.el);
    const top = el('div', 'banner-top', this.panel);
    const what = el('div', 'what', top);
    const line = el('div', 'film-line', what);
    this.stateIcon = el('span', 'film-state', line);
    this.name = el('span', 'film-name', line);
    this.status = el('span', 'film-status', line);
    this.sub = el('div', 'film-sub', what);
    const right = el('div', 'banner-clock', top);
    this.clockView = new Clock(right, false);
    const row = el('div', 'film-bar-row', this.panel);
    this.at = el('span', 'film-at', row);
    this.barBox = el('div', 'bar film-bar', row);
    this.bar = el('div', 'fill acc', this.barBox);
    this.marker = el('div', 'film-marker', this.barBox);
    this.left = el('span', 'film-left', row);
    this.footer = new Footer(this.panel);
    show(this.panel, false);
  }

  enter(): void {
    this.showPanel();
  }

  leave(): void {
    clearTimeout(this.hideTimer);
    clearTimeout(this.drawTimer);
    show(this.panel, false);
    // Leaving the screen, by Back, Exit or Menu, stops the film, keeping
    // how far it got.
    this.ctx.film.stop(true);
  }

  overVideo(): boolean {
    return this.panel.style.display !== 'none';
  }

  /** showPanel shows the panel, which hides by itself unless the film is paused, seeking, starting or waiting. */
  private showPanel(): void {
    const self = this;
    this.draw();
    if (this.panel.style.display === 'none') {
      show(this.panel, true);
      this.ctx.videoChanged();
    }
    clearTimeout(this.hideTimer);
    const f = this.ctx.film;
    if (!f.paused && f.target < 0 && f.phase === 'playing') {
      this.hideTimer = setTimeout(function () {
        self.hidePanel();
      }, panelMs);
    }
    this.tickLater();
  }

  private hidePanel(): void {
    clearTimeout(this.hideTimer);
    clearTimeout(this.drawTimer);
    if (this.panel.style.display !== 'none') {
      show(this.panel, false);
      this.ctx.videoChanged();
    }
  }

  private tickLater(): void {
    const self = this;
    clearTimeout(this.drawTimer);
    this.drawTimer = setTimeout(function () {
      if (self.overVideo()) {
        self.draw();
        self.tickLater();
      }
    }, drawMs);
  }

  private draw(): void {
    const f = this.ctx.film;
    const p: Playing | null = f.item;
    this.clockView.tick();
    if (!p) {
      return;
    }
    const seeking = f.target >= 0;
    const pos = f.position();
    const len = f.duration();
    setHTML(this.stateIcon, f.paused ? icon('pause', 18, 2.2) : icon('play', 18, 2.2));
    setText(this.name, p.programme ? p.programme.programme.title : p.title.name);
    setText(this.status, f.paused ? '' : phaseText(f.phase, f.phaseSeconds()));
    setText(this.sub, subLine(p, f.paused));
    setText(this.at, hms(pos));
    setText(this.left, len > 0 ? '−' + hms(Math.max(0, len - pos)) : '');
    show(this.barBox, len > 0);
    if (len > 0) {
      setWidth(this.bar, seeking ? (f.lastKnown() / len) * 100 : (pos / len) * 100);
      show(this.marker, seeking);
      if (seeking) {
        this.marker.style.left = Math.max(0, Math.min(100, (pos / len) * 100)) + '%';
      }
    }
    this.footer.set(
      [
        { key: 'yellow', text: 'Audio' },
        { key: 'blue', text: 'Subtitles' },
      ],
      [
        { key: 'OK', text: f.paused ? 'Play' : 'Pause' },
        { key: 'lr', text: '−10 s / +30 s' },
        { key: 'CH +/−', text: '5 min' },
        { key: 'Back', text: 'Stop' },
      ],
    );
  }

  key(p: Press): boolean {
    const f = this.ctx.film;
    switch (p.key) {
      case 'ok':
        if (f.target >= 0) {
          f.seekNow();
        } else {
          f.togglePause();
        }
        this.showPanel();
        return true;
      case 'left':
        f.seekBy(-10);
        this.showPanel();
        return true;
      case 'right':
        f.seekBy(30);
        this.showPanel();
        return true;
      case 'pageUp':
      case 'channelNext':
        f.seekBy(300);
        this.showPanel();
        return true;
      case 'pageDown':
      case 'channelPrev':
        f.seekBy(-300);
        this.showPanel();
        return true;
      case 'up':
      case 'down':
      case 'info':
        if (this.overVideo() && p.key === 'info') {
          this.hidePanel();
        } else {
          this.showPanel();
        }
        return true;
      case 'yellow':
      case 'audio':
        this.hidePanel();
        if (this.ctx.film.switchesTrack()) {
          this.chooseTrack();
          return true;
        }
        chooseAudio(this.ctx, thisOne(this.ctx.film.item), this.changed.bind(this), this.ctx.film.streamAudio());
        return true;
      case 'blue':
        this.hidePanel();
        this.chooseSubtitles();
        return true;
      case 'stop':
      case 'back':
        this.ctx.back();
        return true;
    }
    return false;
  }

  /**
   * chooseTrack lets the viewer pick a sound track of a stream that the
   * source's server transcodes, which carries one at a time (D71): the
   * stream starts again with it, from where it was.
   */
  private chooseTrack(): void {
    const self = this;
    const f = this.ctx.film;
    const named = f.streamAudio() || [];
    if (named.length < 2) {
      this.ctx.toast(thisOne(f.item) + ' has one sound track.');
      return;
    }
    this.ctx.pick('Sound', trackItems(named), f.currentTrack(), function (i) {
      if (i >= 0) {
        f.chooseTrack(i);
        self.changed();
      }
    });
  }

  /** chooseSubtitles lets the viewer show subtitles, or none (D69). */
  private chooseSubtitles(): void {
    const self = this;
    const f = this.ctx.film;
    const list = f.subtitles();
    if (!list.length) {
      this.ctx.toast(thisOne(f.item) + ' has no subtitles.');
      return;
    }
    const shown = f.shownSubtitles();
    let chosen = 0;
    const items = [{ label: 'Off', detail: '' }];
    for (let i = 0; i < list.length; i++) {
      const s = list[i]!;
      if (shown && (s.server ? shown.server === s.server : shown.pid === s.pid)) {
        chosen = i + 1;
      }
      items.push(subtitleItem(s, i));
    }
    this.ctx.pick('Subtitles', items, chosen, function (i) {
      if (i >= 0) {
        f.showSubtitles(i === 0 ? null : list[i - 1]!);
        self.changed();
      }
    });
  }

  /** changed redraws the panel after the film paused, went on, or a seek key moved it. */
  changed(): void {
    if (this.ctx.film.item) {
      this.showPanel();
    }
  }

  tick(): void {
    this.clockView.tick();
  }

  lineupChanged(): void {}

  playback(): void {}

  video(): VideoArea {
    return 'full';
  }
}

/** thisOne names what plays, to start a sentence: This film, This episode or This programme. */
function thisOne(p: Playing | null): string {
  if (p && p.programme) {
    return 'This programme';
  }
  return p && p.episode ? 'This episode' : 'This film';
}

/** subLine says more of what plays: an episode's season and name, a programme's channel and when it was on, or that it is paused. */
function subLine(p: Playing, paused: boolean): string {
  if (p.programme) {
    const q = p.programme.programme;
    return label(p.programme.channel) + ' · ' + day(q.start) + ', ' + span(q.start, q.stop) + (paused ? ' · Paused' : '');
  }
  if (p.episode) {
    return 'S' + p.season + ' E' + p.episode.number + (p.episode.name ? ' · ' + p.episode.name : '');
  }
  return paused ? 'Paused' : '';
}

/**
 * phaseText says that a film starts or waits for its source, with how long
 * it has once that is a few seconds; "" while it plays.
 */
export function phaseText(phase: FilmPhase, seconds: number): string {
  const after = seconds >= 3 ? ' ' + seconds + ' s' : '';
  switch (phase) {
    case 'linking':
    case 'starting':
      return 'Starting…' + after;
    case 'waiting':
      return 'Buffering…' + after;
  }
  return '';
}

/** subtitleItem names subtitles in the menu: their language, and what they are. */
function subtitleItem(s: Subtitles, i: number): { label: string; detail: string } {
  const lang = language(s.lang);
  const what: string[] = [];
  if (s.server) {
    if (s.server.title && lang) {
      what.push(s.server.title);
    }
    if (s.server.forced) {
      what.push('Forced');
    }
    if (s.server.hearing) {
      what.push('SDH');
    }
  } else {
    what.push('In the file');
  }
  return { label: lang || (s.server && s.server.title) || 'Subtitles ' + (i + 1), detail: what.join(' · ') };
}
