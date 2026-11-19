// SPDX-License-Identifier: AGPL-3.0-or-later

// The guide: a grid of the programmes on a group's channels, two hours
// across, with the programme in focus above it and the channel that plays
// in a small window beside that. A channel whose source keeps an archive
// plays its past programmes from it (D81): the grid marks them, and goes
// back as far as the archive does. Once the server records (D89), the
// green key records a programme that has not ended, or calls its
// recording off, and the grid marks those to be recorded.

import { type Channel, describe, type Programme } from '../api';
import { el, Logo, setClass, setHTML, setText, show, toggle } from '../dom';
import type { Press } from '../keys';
import { indexOf } from '../lineup';
import { keptPIN } from '../pin';
import { label } from '../player';
import { recordingNow } from '../recordings';
import { clock, date, day, now, span, startOfDay } from '../time';
import { Clock, type Ctx, escape, Footer, type Screen, type VideoArea } from './screen';

/** The grid shows rows channels and spanSec seconds across widthRem. */
const rows = 8;
const spanSec = 2 * 3600;
const widthRem = 59;
const step = 1800;
/** The most programme cells a row draws. */
const cellsPerRow = 10;
/** How far ahead the guide goes, in days, and how many days its strip shows. */
const daysAhead = 7;
/** How far back the guide goes, in days, for a channel with an archive at most; a day for one without. */
const maxDaysBack = 14;

interface Row {
  box: HTMLElement;
  num: HTMLElement;
  logo: Logo;
  grid: HTMLElement;
  cells: HTMLElement[];
}

export class GuideScreen implements Screen {
  el: HTMLElement;
  tunes = true;
  private ctx: Ctx;
  private groupKey = 'all';
  private channels: Channel[] = [];
  /** How many days back the strip of days reaches. */
  private back = 1;
  /** The first row shown, and the row in focus. */
  private top = 0;
  private row = 0;
  /** The grid's first moment, and the moment the focus is at. */
  private start = 0;
  private at = 0;

  private logo: Logo;
  private number: HTMLElement;
  private liveMark: HTMLElement;
  private title: HTMLElement;
  private meta: HTMLElement;
  private description: HTMLElement;
  private frame: HTMLElement;
  private plate: HTMLElement;
  private plateLogo: Logo;
  private days: HTMLElement[] = [];
  private groupChip: HTMLElement;
  private dateLabel: HTMLElement;
  private marks: HTMLElement[] = [];
  private nowChip: HTMLElement;
  private nowLine: HTMLElement;
  private rowsBox: HTMLElement;
  private gridRows: Row[] = [];
  private footer: Footer;
  private clockView: Clock;
  private fetchTimer = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen guide', parent);

    const info = el('div', 'info', this.el);
    const line = el('div', 'info-line', info);
    this.logo = new Logo('info-logo', line);
    this.number = el('span', 'info-number', line);
    this.liveMark = el('span', 'live-mark', line);
    el('span', 'rule', this.liveMark);
    el('span', 'live-dot', this.liveMark);
    el('span', '', this.liveMark, 'LIVE');
    this.title = el('div', 'info-title', info);
    this.meta = el('div', 'info-meta', info);
    this.description = el('div', 'info-description', info);

    this.frame = el('div', 'frame', this.el);
    this.plate = el('div', 'plate', this.frame);
    this.plateLogo = new Logo('plate-logo', this.plate);
    this.clockView = new Clock(this.plate, false);

    const strip = el('div', 'days', this.el);
    for (let i = 0; i < daysAhead; i++) {
      this.days.push(el('span', 'day', strip));
    }
    const groupHint = el('span', 'group-hint', strip);
    el('span', 'swatch yellow', groupHint);
    el('span', '', groupHint, 'Group');
    this.groupChip = el('span', 'group-chip', strip);

    this.dateLabel = el('div', 'ruler-date', this.el);
    const ruler = el('div', 'ruler', this.el);
    for (let i = 0; i < spanSec / step; i++) {
      this.marks.push(el('span', 'mark', ruler));
    }
    this.nowChip = el('span', 'now-chip acc', ruler);

    const body = el('div', 'grid-body', this.el);
    const names = el('div', 'names', body);
    this.rowsBox = el('div', 'cells', body);
    for (let r = 0; r < rows; r++) {
      const box = el('div', 'name-row', names);
      const num = el('span', 'num', box);
      const logo = new Logo('name-logo', box);
      const grid = el('div', 'grid-row', this.rowsBox);
      const cells: HTMLElement[] = [];
      for (let k = 0; k < cellsPerRow; k++) {
        const cell = el('div', 'cell', grid);
        el('span', 'cell-title', cell);
        el('span', 'cell-time', cell);
        cells.push(cell);
      }
      this.gridRows.push({ box: box, num: num, logo: logo, grid: grid, cells: cells });
    }
    this.nowLine = el('div', 'now-line acc', this.rowsBox);

    this.footer = new Footer(this.el);
  }

  enter(arg: any): void {
    const playing = this.ctx.player.channel;
    const want: Channel | null = (arg && arg.channel) || playing;
    if (!this.ctx.lineup.group(this.groupKey)) {
      this.groupKey = 'all';
    }
    this.setGroup(this.groupKey, want ? want.id : 0);
    if (want && indexOf(this.channels, want.id) < 0) {
      this.setGroup('all', want.id);
    }
    const t = now();
    this.start = Math.floor((t - 900) / step) * step;
    this.at = t;
    this.render();
    this.askGuide();
    this.askRecordings(true);
  }

  /** askRecordings reads the household's recordings again, unless they are fresh. */
  private askRecordings(force: boolean): void {
    const self = this;
    this.ctx.recordings.load(function () {
      self.render();
    }, force);
  }

  leave(): void {
    clearTimeout(this.fetchTimer);
  }

  private setGroup(key: string, id: number): void {
    const g = this.ctx.lineup.group(key) || this.ctx.lineup.group('all');
    if (!g) {
      return;
    }
    this.groupKey = g.key;
    this.channels = this.ctx.lineup.list(g);
    // How far back the strip of days reaches: as far as any of the group's
    // channels' archive.
    this.back = 1;
    for (let i = 0; i < this.channels.length; i++) {
      this.back = Math.max(this.back, daysBack(this.channels[i]!));
    }
    const at = id ? indexOf(this.channels, id) : 0;
    this.row = Math.max(0, at);
    this.top = Math.max(0, Math.min(this.row - 2, this.channels.length - rows));
  }

  /** askGuide asks for the programmes of the rows shown, a moment after the view settles. */
  private askGuide(): void {
    const self = this;
    clearTimeout(this.fetchTimer);
    this.fetchTimer = setTimeout(function () {
      const ids: number[] = [];
      for (let i = self.top; i < self.top + rows && i < self.channels.length; i++) {
        ids.push(self.channels[i]!.id);
      }
      self.ctx.guide.want(ids, self.start, self.start + 3 * 3600, function () {
        self.render();
      });
    }, 120);
  }

  private focusedChannel(): Channel | null {
    return this.channels[this.row] || null;
  }

  /** focused returns the programme in focus: the one on at the focus's moment, if any. */
  private focused(): Programme | null {
    const c = this.focusedChannel();
    if (!c) {
      return null;
    }
    const list = this.ctx.guide.between(c.id, this.start, this.start + spanSec);
    for (let i = 0; i < list.length; i++) {
      if (list[i]!.start <= this.at && this.at < list[i]!.stop) {
        return list[i]!;
      }
    }
    return null;
  }

  private x(t: number): number {
    return ((t - this.start) / spanSec) * widthRem;
  }

  render(): void {
    const t = now();
    const end = this.start + spanSec;
    // The ruler.
    setText(this.dateLabel, date(this.start));
    const nowShown = t >= this.start && t < end;
    for (let i = 0; i < this.marks.length; i++) {
      const m = this.marks[i]!;
      const at = this.start + i * step;
      setText(m, clock(at));
      m.style.left = this.x(at) + 'rem';
      // The chip for now covers a mark close to it.
      show(m, !nowShown || Math.abs(this.x(at) - this.x(t)) > 3.5);
    }
    show(this.nowChip, nowShown);
    show(this.nowLine, nowShown);
    if (nowShown) {
      setText(this.nowChip, clock(t));
      this.nowChip.style.left = this.x(t) + 'rem';
      this.nowLine.style.left = this.x(t) + 'rem';
    }
    // The days: from today on, or, while the focus is on a day before
    // today, from as far back as needed to show it.
    const today = startOfDay(t);
    let first = today;
    if (startOfDay(this.at) < today) {
      first = Math.max(today - this.back * 86400, startOfDay(this.at) - 5 * 86400);
    }
    for (let i = 0; i < this.days.length; i++) {
      const d = first + i * 86400 + 3600 * 3; // a time well inside the day, whatever the clock change
      setText(this.days[i]!, day(d));
      setClass(this.days[i]!, 'day' + (startOfDay(d) === startOfDay(this.at) ? ' on' : ''));
    }
    const g = this.ctx.lineup.group(this.groupKey);
    setText(this.groupChip, g ? g.name : '');

    // The rows.
    for (let r = 0; r < rows; r++) {
      const gr = this.gridRows[r]!;
      const i = this.top + r;
      const c = this.channels[i];
      show(gr.box, !!c);
      show(gr.grid, !!c);
      if (!c) {
        continue;
      }
      toggle(gr.box, 'focus', i === this.row);
      setText(gr.num, String(this.ctx.lineup.shownNumber(c, this.groupKey, i)));
      gr.logo.set(c.logo || '', c.name);
      const list = this.ctx.guide.between(c.id, this.start, end);
      const known = this.ctx.guide.knows(c.id, this.start, end);
      let k = 0;
      if (list.length === 0) {
        const cell = gr.cells[0]!;
        this.drawCell(cell, 0, widthRem, known ? 'No guide' : '', '', i === this.row ? 'cell empty focus' : 'cell empty');
        k = 1;
      }
      for (let p = 0; p < list.length && k < cellsPerRow; p++, k++) {
        const q = list[p]!;
        const from = Math.max(q.start, this.start);
        const to = Math.min(q.stop, end);
        let cls = 'cell';
        if (q.stop <= t) {
          cls += inArchive(c, q, t) ? ' past archived' : ' past';
        } else if (q.start <= t) {
          cls += ' airing';
        }
        if (i === this.row && q.start <= this.at && this.at < q.stop) {
          cls += ' focus';
        }
        const rec = q.stop > t ? this.ctx.recordings.find(c.id, q.start) : null;
        if (rec) {
          cls += recordingNow(rec, t) ? ' rec now' : ' rec';
        }
        this.drawCell(gr.cells[k]!, this.x(from), this.x(to) - this.x(from), (q.start < this.start ? '‹ ' : '') + q.title, span(q.start, q.stop), cls);
      }
      for (; k < cellsPerRow; k++) {
        show(gr.cells[k]!, false);
      }
    }
    this.drawInfo();
    this.drawFooter();
  }

  private drawCell(cell: HTMLElement, left: number, width: number, title: string, time: string, cls: string): void {
    show(cell, true);
    setClass(cell, cls);
    cell.style.left = left + 'rem';
    cell.style.width = Math.max(0, width - 0.1875) + 'rem';
    setText(cell.firstChild as HTMLElement, title);
    setText(cell.lastChild as HTMLElement, width > 4 ? time : '');
  }

  private drawInfo(): void {
    const c = this.focusedChannel();
    const playing = this.ctx.player.channel;
    show(this.plate, !playing);
    toggle(this.el, 'with-video', !!playing);
    if (!c) {
      return;
    }
    this.logo.set(c.logo || '', c.name);
    setText(this.number, c.number + ' ' + c.name);
    this.plateLogo.set(c.logo || '', c.name);
    let p = this.focused();
    const details = this.ctx.guide.detailsOf(c.id);
    if (p && details) {
      for (let i = 0; i < details.length; i++) {
        if (details[i]!.id === p.id) {
          p = details[i]!;
        }
      }
    }
    const t = now();
    show(this.liveMark, !!p && p.start <= t && t < p.stop);
    if (!p) {
      setText(this.title, c.name);
      setHTML(this.meta, '');
      setText(this.description, this.ctx.guide.knows(c.id, this.start, this.start + spanSec) ? 'No guide for this time.' : '');
      return;
    }
    setText(this.title, p.title);
    const archived = inArchive(c, p, t);
    const rec = p.stop > t ? this.ctx.recordings.find(c.id, p.start) : null;
    const recText = rec ? (recordingNow(rec, t) ? ' · Recording' : ' · To be recorded') : '';
    setHTML(this.meta, escape(day(p.start) + ', ' + span(p.start, p.stop) + (p.category ? ' · ' + p.category : '') + (archived ? ' · Catch-up' : '') + recText));
    setText(this.description, p.description || p.subtitle || '');
    const self = this;
    if (!details) {
      this.ctx.guide.wantDetails(c.id, function () {
        if (self.focusedChannel() === c) {
          self.drawInfo();
        }
      });
    }
  }

  private drawFooter(): void {
    const c = this.focusedChannel();
    const favourite = !!c && this.ctx.lineup.isFavourite(c.id);
    const p = this.focused();
    const left = [{ key: 'red', text: favourite ? 'Remove Favourite' : 'Favourite' }];
    if (c && p && this.ctx.recordings.on && p.stop > now()) {
      left.push({ key: 'green', text: this.ctx.recordings.find(c.id, p.start) ? 'Cancel Recording' : 'Record' });
    }
    left.push({ key: 'blue', text: 'Search' });
    this.footer.set(
      left,
      [
        { key: 'OK', text: c && p && inArchive(c, p, now()) ? 'Play from the start' : 'Watch' },
        { key: 'lr', text: 'Earlier, later' },
        { key: 'CH +/−', text: 'Page' },
        { key: '0–9', text: 'Go to channel' },
      ],
    );
  }

  key(p: Press): boolean {
    switch (p.key) {
      case 'up':
        this.moveRow(-1);
        break;
      case 'down':
        this.moveRow(1);
        break;
      case 'pageUp':
      case 'channelPrev':
        this.moveRow(-rows);
        break;
      case 'pageDown':
      case 'channelNext':
        this.moveRow(rows);
        break;
      case 'left':
        this.moveTime(-1);
        break;
      case 'right':
        this.moveTime(1);
        break;
      case 'ok':
        this.ok();
        return true;
      case 'red': {
        const c = this.focusedChannel();
        if (c) {
          this.ctx.toggleFavourite(c);
        }
        return true;
      }
      case 'green':
        this.record();
        return true;
      case 'yellow':
        this.chooseGroup();
        return true;
      case 'blue':
        this.ctx.go('search');
        return true;
      case 'guide':
        this.ctx.back();
        return true;
      default:
        return false;
    }
    this.render();
    this.askGuide();
    return true;
  }

  private moveRow(delta: number): void {
    const n = this.channels.length;
    if (n === 0) {
      return;
    }
    if (Math.abs(delta) === 1) {
      this.row = (this.row + delta + n) % n;
    } else {
      this.row = Math.max(0, Math.min(n - 1, this.row + delta));
    }
    if (this.row < this.top) {
      this.top = this.row;
    } else if (this.row >= this.top + rows) {
      this.top = this.row - rows + 1;
    }
    this.top = Math.max(0, Math.min(this.top, n - rows));
    // Keep the focus's moment inside the grid.
    this.at = Math.max(this.at, this.start);
  }

  /** moveTime moves the focus to the programme before or after, or half an hour where there is none. */
  private moveTime(dir: number): void {
    const c = this.focusedChannel();
    const t = now();
    const list = c ? this.ctx.guide.between(c.id, this.start - 6 * 3600, this.start + spanSec + 6 * 3600) : [];
    const p = this.focused();
    let target = 0;
    if (dir > 0) {
      const from = p ? p.stop : this.at + 1;
      for (let i = 0; i < list.length; i++) {
        if (list[i]!.start >= from) {
          target = list[i]!.start;
          break;
        }
      }
      if (!target) {
        target = Math.floor(this.at / step) * step + step;
      }
    } else {
      const before = p ? p.start : this.at;
      for (let i = list.length - 1; i >= 0; i--) {
        if (list[i]!.stop <= before) {
          target = Math.max(list[i]!.start, list[i]!.stop - 60);
          break;
        }
      }
      if (!target) {
        target = Math.ceil(this.at / step) * step - step;
      }
    }
    // The guide reaches back a day, or as far as the channel's archive,
    // and forward a week.
    const earliest = startOfDay(t) - daysBack(c) * 86400;
    const latest = startOfDay(t) + daysAhead * 86400;
    target = Math.max(earliest, Math.min(latest, target));
    this.at = target;
    if (this.at < this.start) {
      this.start = Math.floor(this.at / step) * step;
    } else if (this.at >= this.start + spanSec - step / 2) {
      this.start = Math.floor(this.at / step) * step - step;
    }
  }

  private ok(): void {
    const c = this.focusedChannel();
    if (!c) {
      return;
    }
    const p = this.focused();
    const t = now();
    if (!p || (p.start <= t && t < p.stop)) {
      this.ctx.watch(c, this.channels, true, this.groupKey);
    } else if (p.start > t) {
      this.ctx.toast(p.title + ' starts at ' + clock(p.start) + ' ' + onDay(p.start) + '.');
    } else if (inArchive(c, p, t)) {
      this.ctx.playProgramme(c, p);
    } else if (c.archive) {
      this.ctx.toast(p.title + " is older than this channel's archive.");
    } else {
      this.ctx.toast("This channel's past programmes can't be played: its source keeps no archive of them.");
    }
  }


  /**
   * record records the programme in focus, which has not ended, or calls
   * its recording off. A locked channel's needs the PIN.
   */
  private record(): void {
    const self = this;
    const c = this.focusedChannel();
    const p = this.focused();
    const recordings = this.ctx.recordings;
    if (!c || !p) {
      return;
    }
    if (!recordings.on) {
      this.ctx.toast(describe({ status: 409, code: 'recording_off', retryAfter: 0 }));
      return;
    }
    if (p.stop <= now()) {
      this.ctx.toast(p.title + ' has ended, so it cannot be recorded.');
      return;
    }
    const r = recordings.find(c.id, p.start);
    if (r) {
      recordings.cancel(r, function (err) {
        self.ctx.toast(err ? "Couldn't cancel the recording. " + describe(err) : 'Cancelled the recording of ' + p.title + '.');
        self.render();
      });
      return;
    }
    const schedule = function (pin: string) {
      recordings.record(c.id, p.start, pin, function (err) {
        if (err) {
          self.ctx.toast("Couldn't record " + p.title + '. ' + describe(err));
        } else if (p.start <= now()) {
          self.ctx.toast('Recording ' + p.title + ' until ' + clock(p.stop) + '. It will be on Movies.');
        } else {
          self.ctx.toast(p.title + ' will be recorded at ' + clock(p.start) + ' ' + onDay(p.start) + '. It will be on Movies.');
        }
        self.render();
      });
    };
    if (c.locked) {
      this.ctx.unlock(label(c), function (ok) {
        if (ok) {
          schedule(keptPIN());
        }
      });
      return;
    }
    schedule('');
  }

  private chooseGroup(): void {
    const self = this;
    const groups = this.ctx.lineup.groups;
    const items = [];
    let chosen = 0;
    for (let i = 0; i < groups.length; i++) {
      const g = groups[i]!;
      if (g.key === this.groupKey) {
        chosen = i;
      }
      items.push({ label: g.name, detail: String(g.channels.length) });
    }
    this.ctx.pick('Group', items, chosen, function (i) {
      const g = groups[i];
      if (g) {
        self.setGroup(g.key, 0);
        self.render();
        self.askGuide();
      }
    });
  }

  numbering(): string {
    return this.groupKey;
  }

  tune(c: Channel): void {
    if (indexOf(this.channels, c.id) < 0) {
      this.setGroup('all', c.id);
    } else {
      this.row = indexOf(this.channels, c.id);
      this.moveRow(0);
      this.top = Math.max(0, Math.min(this.row - 2, this.channels.length - rows));
    }
    const t = now();
    this.at = t;
    this.start = Math.floor((t - 900) / step) * step;
    this.render();
    this.askGuide();
  }

  tick(): void {
    this.clockView.tick();
    this.render();
    this.askRecordings(false);
  }

  lineupChanged(): void {
    const c = this.focusedChannel();
    this.setGroup(this.groupKey, c ? c.id : 0);
    this.render();
    this.askGuide();
  }

  favouritesChanged(): void {
    if (this.groupKey === 'fav') {
      const c = this.focusedChannel();
      this.setGroup('fav', c ? c.id : 0);
    }
    this.render();
  }

  playback(): void {
    this.drawInfo();
    this.ctx.videoChanged();
  }

  video(): VideoArea {
    if (!this.ctx.player.channel) {
      return 'none';
    }
    const unit = parseFloat(document.documentElement.style.fontSize) || 16;
    return { x: 56 * unit, y: 2.25 * unit, w: 20 * unit, h: 11.25 * unit };
  }
}

/** inArchive says whether a programme has ended and plays from its channel's archive at t (D81). */
export function inArchive(c: Channel, p: Programme, t: number): boolean {
  return !!c.archive && p.stop <= t && p.start > t - c.archive * 86400;
}

/** onDay says which day a time is on, after a clock time: today, tomorrow, or on Fri 4. */
function onDay(sec: number): string {
  const d = day(sec);
  return d === 'Today' || d === 'Tomorrow' || d === 'Yesterday' ? d.toLowerCase() : 'on ' + d;
}

/** daysBack returns how many days back the guide reaches on a channel: a day, or as far as its archive. */
function daysBack(c: Channel | null): number {
  return Math.max(1, Math.min(maxDaysBack, c && c.archive ? c.archive : 0));
}
