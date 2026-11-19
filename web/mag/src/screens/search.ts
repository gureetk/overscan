// SPDX-License-Identifier: AGPL-3.0-or-later

// Search: an on-screen keyboard, or a keyboard's keys, and what matches:
// channels by name or number, programmes on now or in the next day, and
// films and series by name.

import type { Channel, SearchProgramme, SearchResult, TitleKind, TitleRow } from '../api';
import { el, Logo, setClass, setText } from '../dom';
import type { Press } from '../keys';
import { ListView } from '../list';
import { hiddenPoster, thumb } from '../library';
import { label } from '../player';
import { clock, day, now, span } from '../time';
import { meta } from './library';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';

const columns = 7;
/** The keyboard's keys; the last three are wider: space, delete, clear. */
const letters = 'abcdefghijklmnopqrstuvwxyz0123456789'.split('');
const resultRows = 9;
/** The keyboard's last row: 9, then the wide keys. */
const lastRow = [35, 36, 37, 38];
const maxQuery = 64;

interface Result {
  channel: Channel | null;
  programme: SearchProgramme | null;
  /** A film or a series, in place of a channel. */
  title: TitleRow | null;
  kind: TitleKind;
}

export class SearchScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private query = '';
  private queryText: HTMLElement;
  private keys: HTMLElement[] = [];
  /** The key in focus: 0 to 35 the letters and digits, then 36 space, 37 delete, 38 clear. */
  private focusKey = 0;
  private onResults = false;
  private results: Result[] = [];
  private list: ListView;
  private status: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private timer = 0;
  private seq = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    const self = this;
    this.ctx = ctx;
    this.el = el('div', 'screen search', parent);
    const head = el('div', 'page-head', this.el);
    el('span', 'back', head, '‹ Back');
    el('span', 'page-title', head, 'Search');
    this.clockView = new Clock(head, false);

    const left = el('div', 'search-left', this.el);
    const field = el('div', 'field', left);
    this.queryText = el('span', 'query', field);
    el('span', 'caret acc', field);
    const board = el('div', 'board', left);
    for (let i = 0; i < letters.length; i++) {
      this.keys.push(el('span', 'kb-key', board, letters[i]!.toUpperCase()));
    }
    this.keys.push(el('span', 'kb-key wide', board, 'Space'));
    this.keys.push(el('span', 'kb-key wide', board, 'Delete'));
    this.keys.push(el('span', 'kb-key wide', board, 'Clear'));

    const right = el('div', 'search-right', this.el);
    this.status = el('div', 'status', right);
    const listBox = el('div', 'results', right);
    this.list = new ListView(
      listBox,
      resultRows,
      'result',
      function (row) {
        el('span', 'num', row);
        const logo = new Logo('result-logo', row);
        const text = el('span', 'text', row);
        el('span', 'title', text);
        el('span', 'sub', text);
        (row as any).logo = logo;
      },
      function (row, i) {
        self.drawResult(row, i);
      },
    );
    this.footer = new Footer(this.el);
  }

  enter(): void {
    this.onResults = false;
    this.draw();
    this.list.reset(this.results.length, 0);
    this.list.setActive(false);
  }

  leave(): void {
    clearTimeout(this.timer);
  }

  private draw(): void {
    setText(this.queryText, this.query);
    for (let i = 0; i < this.keys.length; i++) {
      setClass(this.keys[i]!, 'kb-key' + (i >= letters.length ? ' wide' : '') + (!this.onResults && i === this.focusKey ? ' focus' : ''));
    }
    this.list.setActive(this.onResults);
    if (!this.query) {
      setText(this.status, 'Type part of the name of a channel, a programme, a film or a series, or a channel number.');
    } else if (!this.results.length) {
      setText(this.status, 'Nothing matches “' + this.query + '” yet.');
    } else {
      setText(this.status, this.results.length + (this.results.length === 1 ? ' match' : ' matches'));
    }
    this.footer.set(
      [],
      this.onResults
        ? [
            { key: 'OK', text: this.results[this.list.focus()] && this.results[this.list.focus()]!.title ? 'Open' : 'Watch' },
            { key: 'l', text: 'Keyboard' },
          ]
        : [
            { key: 'OK', text: 'Type' },
            { key: 'Back', text: 'Delete' },
            { key: 'r', text: 'Results' },
          ],
    );
  }

  private drawResult(row: HTMLElement, i: number): void {
    const r = this.results[i];
    if (!r) {
      return;
    }
    const parts = row.childNodes;
    const logo = (row as any).logo as Logo;
    const text = parts[2] as HTMLElement;
    setClass(logo.el, 'logo result-logo' + (r.title ? ' poster' : ''));
    if (r.title) {
      setText(parts[0] as HTMLElement, '');
      logo.set(hiddenPoster(r.title) ? '' : thumb(r.title.poster), ''); // the name is beside it
      setText(text.firstChild as HTMLElement, r.title.name);
      const m = meta(r.title);
      setText(text.lastChild as HTMLElement, (r.kind === 'movie' ? 'Film' : 'Series') + (m ? ' · ' + m : '') + (r.title.locked ? ' · Locked' : ''));
      return;
    }
    if (!r.channel) {
      return;
    }
    setText(parts[0] as HTMLElement, String(r.channel.number));
    logo.set(r.channel.logo || '', r.channel.name);
    if (r.programme) {
      setText(text.firstChild as HTMLElement, r.programme.title);
      const t = now();
      const when = r.programme.start <= t ? 'On now, ' + span(r.programme.start, r.programme.stop) : day(r.programme.start) + ', ' + clock(r.programme.start);
      setText(text.lastChild as HTMLElement, when + ' · ' + r.channel.name);
    } else {
      setText(text.firstChild as HTMLElement, r.channel.name);
      const p = this.ctx.guide.at(r.channel.id, now());
      setText(text.lastChild as HTMLElement, p ? 'Now: ' + p.title : 'Channel');
    }
  }

  private type(s: string): void {
    if (this.query.length + s.length <= maxQuery) {
      this.query += s;
      this.changed();
    }
  }

  private changed(): void {
    const self = this;
    this.draw();
    clearTimeout(this.timer);
    if (!this.query.replace(/\s+/g, '')) {
      this.results = [];
      this.list.reset(0, 0);
      this.draw();
      return;
    }
    this.timer = setTimeout(function () {
      self.ask();
    }, 400);
  }

  private ask(): void {
    const self = this;
    const seq = ++this.seq;
    const q = this.query.replace(/^\s+|\s+$/g, '');
    this.ctx.client.call<SearchResult>('GET', 'search?q=' + encodeURIComponent(q), null, function (err, res) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !res) {
        setText(self.status, "The search didn't work. Try again.");
        return;
      }
      const out: Result[] = [];
      const ids: number[] = [];
      for (let i = 0; i < res.channels.length; i++) {
        const c = self.ctx.lineup.channel(res.channels[i]!);
        if (c) {
          out.push({ channel: c, programme: null, title: null, kind: 'movie' });
          ids.push(c.id);
        }
      }
      for (let i = 0; i < res.programmes.length; i++) {
        const p = res.programmes[i]!;
        const c = self.ctx.lineup.channel(p.channel);
        if (c) {
          out.push({ channel: c, programme: p, title: null, kind: 'movie' });
        }
      }
      const movies = res.movies || [];
      const series = res.series || [];
      for (let i = 0; i < movies.length; i++) {
        out.push({ channel: null, programme: null, title: movies[i]!, kind: 'movie' });
      }
      for (let i = 0; i < series.length; i++) {
        out.push({ channel: null, programme: null, title: series[i]!, kind: 'series' });
      }
      self.results = out;
      self.list.reset(out.length, 0);
      if (!out.length) {
        self.onResults = false;
      }
      self.draw();
      self.ctx.guide.wantNow(ids.slice(0, 50), function () {
        self.list.render();
      });
    });
  }

  key(p: Press): boolean {
    if (p.key === 'char') {
      this.type(p.char);
      return true;
    }
    if (p.key === 'digit') {
      this.type(String(p.digit));
      return true;
    }
    if (this.onResults) {
      return this.resultsKey(p);
    }
    const k = this.focusKey;
    switch (p.key) {
      case 'left':
        if (k >= lastRow[0]!) {
          this.focusKey = lastRow[(lastRow.indexOf(k) + lastRow.length - 1) % lastRow.length]!;
        } else {
          this.focusKey = k % columns === 0 ? k + columns - 1 : k - 1;
        }
        break;
      case 'right':
        if (k === lastRow[lastRow.length - 1] || (k < lastRow[0]! && k % columns === columns - 1)) {
          if (this.results.length) {
            this.onResults = true;
          }
        } else if (k >= lastRow[0]!) {
          this.focusKey = lastRow[lastRow.indexOf(k) + 1]!;
        } else {
          this.focusKey = k + 1;
        }
        break;
      case 'up':
        if (k >= letters.length) {
          this.focusKey = letters.length - columns + (k - letters.length) * 2;
        } else if (k >= columns) {
          this.focusKey = k - columns;
        }
        break;
      case 'down':
        if (k < letters.length) {
          this.focusKey = k + columns < letters.length ? k + columns : letters.length + Math.min(2, Math.floor((k % columns) / 2.4));
        }
        break;
      case 'ok':
        if (k < letters.length) {
          this.type(letters[k]!);
        } else if (k === letters.length) {
          this.type(' ');
        } else if (k === letters.length + 1) {
          this.query = this.query.slice(0, -1);
          this.changed();
        } else {
          this.query = '';
          this.changed();
        }
        return true;
      case 'back':
        if (this.query) {
          this.query = this.query.slice(0, -1);
          this.changed();
          return true;
        }
        return false;
      default:
        return false;
    }
    this.draw();
    return true;
  }

  private resultsKey(p: Press): boolean {
    switch (p.key) {
      case 'up':
        this.list.move(-1);
        this.draw();
        return true;
      case 'down':
        this.list.move(1);
        this.draw();
        return true;
      case 'pageUp':
      case 'channelPrev':
        this.list.page(-1);
        this.draw();
        return true;
      case 'pageDown':
      case 'channelNext':
        this.list.page(1);
        this.draw();
        return true;
      case 'left':
      case 'back':
        this.onResults = false;
        this.draw();
        return true;
      case 'ok':
        this.open();
        return true;
      case 'red': {
        const r = this.results[this.list.focus()];
        if (r && r.channel) {
          this.ctx.toggleFavourite(r.channel);
        }
        return true;
      }
    }
    return false;
  }

  private open(): void {
    const r = this.results[this.list.focus()];
    if (!r) {
      return;
    }
    if (r.title) {
      this.ctx.go('title', { kind: r.kind, id: r.title.id, name: r.title.name });
      return;
    }
    if (!r.channel) {
      return;
    }
    const channel = r.channel;
    const t = now();
    const p = r.programme;
    if (p && p.start > t) {
      this.ctx.toast(p.title + ' starts at ' + clock(p.start) + ', ' + day(p.start).toLowerCase() + ', on ' + label(channel) + '.');
      return;
    }
    const list: Channel[] = [];
    for (let i = 0; i < this.results.length; i++) {
      const x = this.results[i]!;
      if (x.channel && !x.programme) {
        list.push(x.channel);
      }
    }
    this.ctx.watch(channel, list.indexOf(channel) >= 0 ? list : [channel], true, '');
  }

  tick(): void {
    this.clockView.tick();
  }

  lineupChanged(): void {
    if (this.query) {
      this.ask();
    }
  }

  playback(): void {}

  video(): VideoArea {
    return 'none';
  }
}

