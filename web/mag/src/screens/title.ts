// SPDX-License-Identifier: AGPL-3.0-or-later

// A film or a series, opened: its picture, what its source says about it,
// and what OK plays. A film goes on from where the account got to, or
// plays from the start; a series plays the episode that comes next, or
// opens its episodes (D65).

import { describe, type EpisodeInfo, type Title, type TitleKind } from '../api';
import { el, Logo, setClass, setText, show } from '../dom';
import type { Press } from '../keys';
import { duration, hms } from '../time';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';

interface Button {
  text: string;
  act: () => void;
}

/** The arguments of go('title'). */
export interface TitleArg {
  kind: TitleKind;
  id: number;
  name: string;
}

/** findEpisode returns an episode of a series by its ID, and its season's number. */
export function findEpisode(t: Title, id: number): { episode: EpisodeInfo; season: number } | null {
  const seasons = t.seasons || [];
  for (let i = 0; i < seasons.length; i++) {
    const s = seasons[i]!;
    for (let j = 0; j < s.episodes.length; j++) {
      if (s.episodes[j]!.id === id) {
        return { episode: s.episodes[j]!, season: s.number };
      }
    }
  }
  return null;
}

export class TitleScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private arg: TitleArg | null = null;
  private title: Title | null = null;
  private buttons: Button[] = [];
  private focusButton = 0;
  private seq = 0;

  private backdrop: HTMLElement;
  private poster: Logo;
  private name: HTMLElement;
  private metaLine: HTMLElement;
  private buttonRow: HTMLElement;
  private plot: HTMLElement;
  private credits: HTMLElement;
  private note: HTMLElement;
  private footer: Footer;
  private clockView: Clock;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen title-screen', parent);
    this.backdrop = el('div', 'backdrop', this.el);
    el('div', 'backdrop-shade', this.el);
    const head = el('div', 'page-head', this.el);
    el('span', 'back', head, '‹ Back');
    el('span', 'page-title', head, '');
    this.clockView = new Clock(head, false);
    const frame = el('div', 'title-poster', this.el);
    this.poster = new Logo('side-poster', frame);
    const info = el('div', 'title-info', this.el);
    this.name = el('div', 'title-name', info);
    this.metaLine = el('div', 'title-meta', info);
    this.buttonRow = el('div', 'title-buttons', info);
    this.plot = el('div', 'title-plot', info);
    this.credits = el('div', 'title-credits', info);
    this.note = el('div', 'title-note', info);
    this.footer = new Footer(this.el);
  }

  enter(arg: any): void {
    if (arg && arg.id) {
      const a = arg as TitleArg;
      if (!this.arg || this.arg.id !== a.id || this.arg.kind !== a.kind) {
        this.title = null;
        this.focusButton = 0;
        this.backdrop.style.backgroundImage = '';
      }
      this.arg = a;
    }
    // Asked for again on the way back, too: the progress may have changed.
    this.load();
    this.draw();
  }

  leave(): void {
    this.seq++;
  }

  private load(): void {
    const self = this;
    const a = this.arg;
    if (!a) {
      return;
    }
    const seq = ++this.seq;
    const model = a.kind === 'movie' ? this.ctx.movies : this.ctx.series;
    model.open(a.id, function (err, t) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !t) {
        if (err && err.code === 'not_found') {
          self.ctx.toast(a.name + ' is no longer in your source.');
          self.ctx.back();
          return;
        }
        setText(self.note, "Couldn't open " + a.name + '. ' + describe(err));
        return;
      }
      self.title = t;
      self.draw();
    });
  }

  private draw(): void {
    const a = this.arg;
    const t = this.title;
    this.clockView.tick();
    setText(this.el.querySelector('.page-title') as HTMLElement, a ? (a.kind === 'movie' ? 'Movies' : 'Series') : '');
    if (!t) {
      setText(this.name, a ? a.name : '');
      setText(this.metaLine, '');
      setText(this.plot, 'Loading…');
      setText(this.credits, '');
      setText(this.note, '');
      this.poster.set('', a ? a.name : '');
      this.buttons = [];
      this.drawButtons();
      return;
    }
    this.poster.set(t.poster || '', t.name);
    if (t.backdrop) {
      const url = 'url("' + t.backdrop + '")';
      if (this.backdrop.style.backgroundImage.indexOf(t.backdrop) < 0) {
        this.backdrop.style.backgroundImage = url;
      }
    }
    setText(this.name, t.name);
    setText(this.metaLine, this.metaOf(t));
    setText(this.plot, t.plot || (t.details ? '' : 'Your source says nothing more about it.'));
    const credits: string[] = [];
    if (t.director) {
      credits.push('Director: ' + t.director);
    }
    if (t.cast) {
      credits.push('Cast: ' + t.cast);
    }
    setText(this.credits, credits.join('\n'));
    setText(this.note, t.details_error ? "Couldn't read its details from your source just now: " + t.details_error + '.' : '');
    this.buttons = t.kind === 'movie' ? this.filmButtons(t) : this.seriesButtons(t);
    this.focusButton = Math.min(this.focusButton, this.buttons.length - 1);
    this.drawButtons();
  }

  private metaOf(t: Title): string {
    const parts: string[] = [];
    if (t.year) {
      parts.push(String(t.year));
    }
    if (t.kind === 'series' && t.seasons && t.seasons.length) {
      parts.push(t.seasons.length + (t.seasons.length === 1 ? ' season' : ' seasons'));
    } else if (t.runtime) {
      parts.push(duration(t.runtime * 60));
    }
    if (t.rating) {
      parts.push('★ ' + (t.rating / 10).toFixed(1));
    }
    if (t.age_rating) {
      parts.push(/^\d+\+?$/.test(t.age_rating) ? 'Age ' + t.age_rating : t.age_rating);
    }
    if (t.genre) {
      parts.push(t.genre);
    }
    return parts.join(' · ');
  }

  private filmButtons(t: Title): Button[] {
    const self = this;
    const p = t.progress;
    const play = function (from: number) {
      return function () {
        self.ctx.playFilm({ title: t, episode: null, season: 0 }, from);
      };
    };
    if (p && !p.finished && p.position > 0) {
      return [
        { text: 'Resume from ' + hms(p.position), act: play(p.position) },
        { text: 'Play from the Start', act: play(0) },
      ];
    }
    return [{ text: p && p.finished ? 'Play Again' : 'Play', act: play(0) }];
  }

  private seriesButtons(t: Title): Button[] {
    const self = this;
    const out: Button[] = [];
    const found = t.next ? findEpisode(t, t.next.episode) : null;
    if (found) {
      const at = t.next!.position;
      const label = 'S' + found.season + ' E' + found.episode.number;
      out.push({
        text: at > 0 ? 'Resume ' + label + ' from ' + hms(at) : 'Play ' + label,
        act: function () {
          self.ctx.playFilm({ title: t, episode: found.episode, season: found.season }, at);
        },
      });
    }
    if (t.seasons && t.seasons.length) {
      out.push({
        text: 'Episodes',
        act: function () {
          self.ctx.go('episodes', { title: t, episode: found ? found.episode.id : 0 });
        },
      });
    }
    return out;
  }

  private drawButtons(): void {
    while (this.buttonRow.childNodes.length > this.buttons.length) {
      this.buttonRow.removeChild(this.buttonRow.lastChild!);
    }
    while (this.buttonRow.childNodes.length < this.buttons.length) {
      el('span', 'title-button', this.buttonRow);
    }
    for (let i = 0; i < this.buttons.length; i++) {
      const b = this.buttonRow.childNodes[i] as HTMLElement;
      setClass(b, 'title-button' + (i === 0 ? ' primary' : '') + (i === this.focusButton ? ' focus' : ''));
      setText(b, this.buttons[i]!.text);
    }
    show(this.buttonRow, this.buttons.length > 0);
    this.footer.set(
      [{ key: 'blue', text: 'Search' }],
      this.buttons.length > 1
        ? [
            { key: 'OK', text: this.buttons[this.focusButton]!.text },
            { key: 'lr', text: 'Choose' },
          ]
        : this.buttons.length
          ? [{ key: 'OK', text: this.buttons[0]!.text }]
          : [],
    );
  }

  key(p: Press): boolean {
    switch (p.key) {
      case 'left':
        this.focusButton = Math.max(0, this.focusButton - 1);
        this.drawButtons();
        return true;
      case 'right':
        this.focusButton = Math.min(this.buttons.length - 1, this.focusButton + 1);
        this.drawButtons();
        return true;
      case 'ok': {
        const b = this.buttons[this.focusButton];
        if (b) {
          b.act();
        }
        return true;
      }
      case 'blue':
        this.ctx.go('search');
        return true;
      case 'up':
      case 'down':
        return true;
    }
    return false;
  }

  tick(): void {
    this.clockView.tick();
  }

  lineupChanged(): void {}

  playback(): void {}

  video(): VideoArea {
    return 'none';
  }
}
