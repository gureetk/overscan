// SPDX-License-Identifier: AGPL-3.0-or-later

// Recordings (D100), from Home while the server records: the household's
// recordings under way, those to come, and those of the last day that are
// done or failed. Green stops one under way, which keeps what it recorded,
// or calls off one to come; OK plays its channel, or opens Movies for one
// that is done. Its first row records a channel by time (D101): a channel,
// a day up to a week ahead, a start and an end.

import { type Channel, describe } from '../api';
import { el, setHTML, setText } from '../dom';
import { arrows, icon } from '../icons';
import type { Press } from '../keys';
import { label } from '../player';
import { exact, type Recording } from '../recordings';
import { clock, date, day, duration, now, onDay, span } from '../time';
import { stop, withPIN } from './record';
import { Clock, type Ctx, escape, Footer, type Hint, type Screen, type VideoArea } from './screen';

type Section = 'now' | 'later' | 'ended';

/** A row of the list: Record by Time, a section's heading, or a recording. */
interface Row {
  make?: boolean;
  head?: string;
  r?: Recording;
  section?: Section;
}

interface Info {
  icon: string;
  title: string;
  text: string;
  facts: [string, string][];
}

/** A recording by time as the box sets it (D101). */
interface Form {
  channel: Channel;
  /** Days after today, 0 to lastDay. */
  day: number;
  /** Minutes after midnight. */
  start: number;
  end: number;
  /** The row in focus: the channel, the day, the start, the end, or Record. */
  row: number;
  /** Digits typed on the row in focus, not yet taken. */
  typed: string;
}

/** The form's rows. */
const formRows = ['Channel', 'Day', 'Starts', 'Ends', 'Record'];

/** How many rows, headings with them, the list shows at once. */
const shown = 10;

/** How often the screen reads the list again while it shows, in seconds. */
const refreshEvery = 30;

/** The last day a recording by time may begin, after today. */
const lastDay = 6;

/** The focus's place for the row that records by time. */
const makeID = -1;

/** The most hours of the guide that the form asks for. */
const guideHours = 6;

/** size writes a file's size: 850 MB, or 1.4 GB. */
export function size(bytes: number): string {
  if (bytes >= 1e9) {
    return (Math.round(bytes / 1e8) / 10).toFixed(1) + ' GB';
  }
  return Math.max(1, Math.round(bytes / 1e6)) + ' MB';
}

/** at returns the Unix time minutes after the box's midnight, days after today. */
function at(days: number, minutes: number): number {
  const d = new Date(now() * 1000);
  d.setHours(0, 0, 0, 0);
  d.setDate(d.getDate() + days);
  d.setHours(Math.floor(minutes / 60), minutes % 60, 0, 0);
  return d.getTime() / 1000;
}

/** times returns when a recording by time begins and ends: an end before its start is on the next day. */
function times(f: Form): { start: number; stop: number } {
  return { start: at(f.day, f.start), stop: at(f.end > f.start ? f.day : f.day + 1, f.end) };
}

/** dayName names a day after today: Today, Tomorrow, or Sun 22 Nov. */
function dayName(days: number): string {
  return days === 0 ? 'Today' : days === 1 ? 'Tomorrow' : date(at(days, 12 * 60));
}

/** typedTime shows digits typed for a time: 21:3_. */
function typedTime(typed: string): string {
  const d = (typed + '____').slice(0, 4);
  return d.slice(0, 2) + ':' + d.slice(2);
}

export class RecordingsScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private rows: Row[] = [];
  /** The place in rows of the row in focus, or -1 when there is none. */
  private focus = -1;
  /** The recording in focus, or makeID, kept across a new list. */
  private focusID = 0;
  /** The first row shown. */
  private top = 0;
  private readAt = 0;
  /** The recording by time being set, while its form shows. */
  private form: Form | null = null;
  private typedTimer = 0;
  private guideTimer = 0;

  private title: HTMLElement;
  private infoIcon: HTMLElement;
  private infoTitle: HTMLElement;
  private infoText: HTMLElement;
  private infoFacts: HTMLElement;
  private list: HTMLElement;
  private footer: Footer;
  private clockView: Clock;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen settings recordings', parent);
    const head = el('div', 'page-head', this.el);
    el('span', 'back', head, '‹ Home');
    this.title = el('span', 'page-title', head, 'Recordings');
    this.clockView = new Clock(head, false);
    const info = el('div', 'settings-info', this.el);
    this.infoIcon = el('div', 'info-icon', info);
    this.infoTitle = el('div', 'info-title', info);
    this.infoText = el('div', 'info-text', info);
    this.infoFacts = el('div', 'info-facts', info);
    this.list = el('div', 'settings-list', this.el);
    this.footer = new Footer(this.el);
  }

  enter(): void {
    this.focusID = 0;
    this.focus = -1;
    this.top = 0;
    this.form = null;
    this.read();
    this.draw();
  }

  leave(): void {
    this.form = null;
    clearTimeout(this.typedTimer);
    clearTimeout(this.guideTimer);
  }

  /** read asks the server for the list again, and draws it once it has it. */
  private read(): void {
    const self = this;
    this.readAt = now();
    this.ctx.recordings.load(function () {
      self.draw();
    }, true);
  }

  /** build makes the rows from the list, and finds the row in focus again. */
  private build(): void {
    const t = now();
    const recordings = this.ctx.recordings;
    const rows: Row[] = [];
    if (recordings.on) {
      rows.push({ make: true });
    }
    const add = function (head: string, list: Recording[], section: Section): void {
      if (!list.length) {
        return;
      }
      rows.push({ head: head });
      for (let i = 0; i < list.length; i++) {
        rows.push({ r: list[i]!, section: section });
      }
    };
    add('Recording Now', recordings.underWay(t), 'now');
    add('Coming Up', recordings.toCome(t), 'later');
    add('Recorded Lately', recordings.recent, 'ended');
    this.rows = rows;
    let found = -1;
    for (let i = 0; i < rows.length; i++) {
      if (this.idOf(rows[i]!) === this.focusID && this.focusID !== 0) {
        found = i;
        break;
      }
    }
    if (found < 0) {
      // The row in focus has gone: the next one from its place, else the one before.
      const from = Math.max(this.focus, 0);
      for (let i = from; i < rows.length && found < 0; i++) {
        if (this.focusable(rows[i]!)) {
          found = i;
        }
      }
      for (let i = Math.min(from, rows.length - 1); i >= 0 && found < 0; i--) {
        if (this.focusable(rows[i]!)) {
          found = i;
        }
      }
    }
    this.focus = found;
    this.focusID = found >= 0 ? this.idOf(rows[found]!) : 0;
  }

  private focusable(row: Row): boolean {
    return !!row.make || !!row.r;
  }

  private idOf(row: Row): number {
    return row.make ? makeID : row.r ? row.r.id : 0;
  }

  /** move moves the focus by delta rows that take it, skipping headings. */
  private move(delta: number): void {
    if (this.focus < 0) {
      return;
    }
    const step = delta < 0 ? -1 : 1;
    let left = Math.abs(delta);
    let found = this.focus;
    for (let i = this.focus + step; i >= 0 && i < this.rows.length && left > 0; i += step) {
      if (this.focusable(this.rows[i]!)) {
        found = i;
        left--;
      }
    }
    this.focus = found;
    this.focusID = this.idOf(this.rows[found]!);
  }

  /** place chooses the first row shown, so that the focus and the heading above it show. */
  private place(): void {
    const n = this.rows.length;
    if (this.focus < 0) {
      this.top = 0;
      return;
    }
    let first = this.focus;
    if (first > 0 && this.rows[first - 1]!.head) {
      first--;
    }
    if (first < this.top) {
      this.top = first;
    } else if (this.focus >= this.top + shown) {
      this.top = this.focus - shown + 1;
    }
    this.top = Math.max(0, Math.min(this.top, n - shown));
  }

  private draw(): void {
    if (this.form) {
      this.drawForm();
      return;
    }
    setText(this.title, 'Recordings');
    this.build();
    this.place();
    const n = this.rows.length;
    const end = Math.min(n, this.top + shown);
    let html = '';
    if (!n) {
      html = '<div class="setting off"><span class="label">Nothing is recording, or to come</span></div>';
    }
    const above = this.count(0, this.top);
    if (above) {
      html += '<div class="setting-more">' + above + ' more above</div>';
    }
    for (let i = this.top; i < end; i++) {
      const row = this.rows[i]!;
      const focus = i === this.focus ? ' focus' : '';
      if (row.make) {
        html +=
          '<div class="setting' +
          focus +
          '">' +
          icon('add', 17) +
          '<span class="label">Record by Time</span><span class="value">A channel, a day and a time</span><span class="chev">›</span></div>';
        continue;
      }
      if (row.head) {
        html += '<div class="setting-head' + (i > this.top ? ' gap' : '') + '">' + escape(row.head) + '</div>';
        continue;
      }
      const r = row.r!;
      html +=
        '<div class="setting' +
        focus +
        '">' +
        icon(this.iconOf(row), 17) +
        '<span class="label">' +
        escape(r.title) +
        '</span><span class="value">' +
        escape(this.valueOf(row)) +
        '</span></div>';
    }
    const below = this.count(end, n);
    if (below) {
      html += '<div class="setting-more">' + below + ' more below</div>';
    }
    setHTML(this.list, html);
    const row = this.focus >= 0 ? this.rows[this.focus]! : null;
    this.drawInfo(row ? (row.make ? this.makeInfo() : this.info(row)) : this.empty());
    this.drawFooter();
  }

  /** count counts the recordings among rows from to to. */
  private count(from: number, to: number): number {
    let k = 0;
    for (let i = from; i < to; i++) {
      if (this.rows[i]!.r) {
        k++;
      }
    }
    return k;
  }

  private iconOf(row: Row): string {
    if (row.section === 'now') {
      return 'record';
    }
    if (row.section === 'later') {
      return 'guide';
    }
    return row.r!.state === 'done' ? 'check' : 'about';
  }

  /** channelName names a recording's channel, or says nothing for one that left the lineup. */
  private channelName(r: Recording): string {
    const c = this.ctx.lineup.channel(r.channel);
    return c ? label(c) : '';
  }

  private valueOf(row: Row): string {
    const r = row.r!;
    const c = this.ctx.lineup.channel(r.channel);
    const name = c ? c.name + ' · ' : '';
    if (row.section === 'now') {
      return name + (r.until_stopped ? 'until stopped' : 'until ' + clock(r.stop));
    }
    if (row.section === 'later') {
      return name + day(r.start) + ' ' + span(r.start, r.stop);
    }
    return name + (r.state === 'done' ? 'Recorded' + (r.size ? ', ' + size(r.size) : '') : 'Failed');
  }

  private info(row: Row): Info {
    const r = row.r!;
    const channel = this.channelName(r);
    const facts: [string, string][] = [];
    if (channel) {
      facts.push(['Channel', channel]);
    }
    if (row.section === 'now') {
      facts.push(['Started', clock(r.start)]);
      facts.push(['Ends', r.until_stopped ? 'When stopped, by ' + clock(r.stop) : clock(exact(r) ? r.stop : r.stop + 120)]);
      let text = 'Recording until two minutes after the programme ends. Green stops it sooner; either way, what it recorded will be on Movies.';
      if (r.until_stopped) {
        text = 'Recording until you stop it, or until ' + clock(r.stop) + ' at the latest. Green stops it, and what it recorded will be on Movies.';
      } else if (r.timed) {
        text = 'Recording until ' + clock(r.stop) + ', as set. Green stops it sooner; either way, what it recorded will be on Movies.';
      }
      return { icon: 'record', title: r.title, text: text, facts: facts };
    }
    if (row.section === 'later') {
      facts.push(['Day', date(r.start)]);
      facts.push(['Time', span(r.start, r.stop)]);
      return {
        icon: 'guide',
        title: r.title,
        text: r.timed
          ? 'Will be recorded from ' + clock(r.start) + ' to ' + clock(r.stop) + ', as set, then be on Movies. Green cancels it.'
          : 'Will be recorded from a minute before the programme to two minutes after it, then be on Movies. Green cancels it.',
        facts: facts,
      };
    }
    facts.push(['Began', day(r.start) + ' ' + clock(r.start)]);
    if (r.size) {
      facts.push(['Size', size(r.size)]);
    }
    if (r.state === 'done') {
      return { icon: 'check', title: r.title, text: 'Recorded. It is on Movies, on the Recordings shelf: OK opens Movies.', facts: facts };
    }
    return {
      icon: 'about',
      title: r.title,
      text: 'This recording failed' + (r.error ? ': ' + r.error : '') + '.' + (r.size ? ' What it recorded before then is on Movies.' : ''),
      facts: facts,
    };
  }

  private makeInfo(): Info {
    return {
      icon: 'add',
      title: 'Record by Time',
      text: 'Records a channel from a start to an end that you choose, today or up to a week ahead, for at most 24 hours: exactly as set, whatever the guide says. Then it is on Movies.',
      facts: [],
    };
  }

  private empty(): Info {
    return {
      icon: 'record',
      title: 'Recordings',
      text:
        'Nothing is recording now, and nothing is to come. Green or REC records the channel you watch, in Live TV or full screen, and a programme in the TV Guide. What is recorded plays on Movies, on the Recordings shelf.',
      facts: [],
    };
  }

  private drawInfo(info: Info): void {
    setHTML(this.infoIcon, icon(info.icon, 68, 1.4));
    setText(this.infoTitle, info.title);
    setText(this.infoText, info.text);
    let html = '';
    for (let i = 0; i < info.facts.length; i++) {
      html += '<div class="fact"><span>' + escape(info.facts[i]![0]) + '</span><span class="v">' + escape(info.facts[i]![1]) + '</span></div>';
    }
    setHTML(this.infoFacts, html);
  }

  private drawFooter(): void {
    const row = this.focus >= 0 ? this.rows[this.focus]! : null;
    const left: Hint[] = [];
    const right: Hint[] = [];
    if (row && row.section === 'now') {
      left.push({ key: 'green', text: 'Stop Recording' });
    } else if (row && row.section === 'later') {
      left.push({ key: 'green', text: 'Cancel Recording' });
    }
    if (row && row.make) {
      right.push({ key: 'OK', text: 'Choose' });
    } else if (row && row.section !== 'ended' && this.ctx.lineup.channel(row.r!.channel)) {
      right.push({ key: 'OK', text: 'Watch the Channel' });
    } else if (row && row.r && row.r.state === 'done') {
      right.push({ key: 'OK', text: 'Movies' });
    }
    if (this.rows.length > 1) {
      right.push({ key: 'ud', text: 'Move' });
    }
    this.footer.set(left, right);
  }

  /** open plays the channel of a recording under way or to come, opens Movies for one that is done, or opens the form. */
  private open(): void {
    const row = this.focus >= 0 ? this.rows[this.focus]! : null;
    if (!row) {
      return;
    }
    if (row.make) {
      this.openForm();
      return;
    }
    const r = row.r!;
    if (row.section === 'ended') {
      if (r.state === 'done') {
        this.ctx.go('movies');
      }
      return;
    }
    const c = this.ctx.lineup.channel(r.channel);
    if (c) {
      this.ctx.watch(c, this.ctx.lineup.channels, true, 'all');
    }
  }

  /** stopFocused stops the recording in focus, or calls it off. */
  private stopFocused(): void {
    const row = this.focus >= 0 ? this.rows[this.focus]! : null;
    if (!row || !row.r || row.section === 'ended') {
      return;
    }
    const self = this;
    // The list read again keeps the focus on a stopped recording, which
    // moves to those that ended.
    stop(this.ctx, row.r, function () {
      self.read();
    });
  }

  // Recording by time (D101).

  /** openForm shows the form, for the channel playing or watched last, from the next five minutes for an hour. */
  private openForm(): void {
    const lineup = this.ctx.lineup;
    const c = this.ctx.player.channel || lineup.lastWatched() || lineup.channels[0];
    if (!c) {
      this.ctx.toast('There are no channels to record.');
      return;
    }
    const d = new Date(now() * 1000);
    let minutes = Math.ceil((d.getHours() * 60 + d.getMinutes() + 1) / 5) * 5;
    let days = 0;
    if (minutes >= 24 * 60) {
      minutes -= 24 * 60;
      days = 1;
    }
    this.form = { channel: c, day: days, start: minutes, end: (minutes + 60) % (24 * 60), row: 0, typed: '' };
    this.wantGuide();
    this.draw();
  }

  private drawForm(): void {
    const f = this.form!;
    setText(this.title, 'Record by Time');
    const w = times(f);
    const values = [
      f.row === 0 && f.typed ? f.typed + '_' : label(f.channel),
      dayName(f.day),
      f.row === 2 && f.typed ? typedTime(f.typed) : clock(w.start),
      f.row === 3 && f.typed ? typedTime(f.typed) : clock(w.stop) + (f.end > f.start ? '' : ', next day'),
    ];
    let html = '';
    for (let i = 0; i < formRows.length; i++) {
      const focus = i === f.row ? ' focus' : '';
      if (i === formRows.length - 1) {
        html +=
          '<div class="setting gap' +
          focus +
          '">' +
          icon('record', 17) +
          '<span class="label">Record</span><span class="value">' +
          escape(duration(w.stop - Math.max(w.start, now()))) +
          '</span><span class="chev">›</span></div>';
        continue;
      }
      html +=
        '<div class="setting' +
        focus +
        '"><span class="label">' +
        escape(formRows[i]!) +
        '</span><span class="arrow">' +
        arrows('l') +
        '</span><span class="choice">' +
        escape(values[i]!) +
        '</span><span class="arrow">' +
        arrows('r') +
        '</span></div>';
    }
    setHTML(this.list, html);
    const t = now();
    let text: string;
    if (w.stop <= t) {
      text = 'That time has passed. Choose a later one.';
    } else if (w.start <= t) {
      text = 'Records ' + label(f.channel) + ' from now until ' + clock(w.stop) + ', ' + duration(w.stop - t) + '. It will be on Movies.';
    } else {
      text =
        'Records ' +
        label(f.channel) +
        ' ' +
        onDay(w.start) +
        ' from ' +
        clock(w.start) +
        ' to ' +
        clock(w.stop) +
        ', ' +
        duration(w.stop - w.start) +
        '. It will be on Movies.';
    }
    const facts: [string, string][] = [];
    const ps = this.ctx.guide.between(f.channel.id, w.start, w.stop);
    for (let i = 0; i < ps.length && i < 4; i++) {
      facts.push([clock(ps[i]!.start), ps[i]!.title]);
    }
    if (ps.length > 4) {
      facts.push(['', 'and ' + (ps.length - 4) + ' more']);
    }
    this.drawInfo({ icon: 'record', title: 'Record by Time', text: text, facts: facts });
    const right: Hint[] = [];
    if (f.row < formRows.length - 1) {
      right.push({ key: 'lr', text: 'Change' });
    }
    if (f.row === 0) {
      right.push({ key: '0–9', text: 'Channel Number' });
    } else if (f.row === 2 || f.row === 3) {
      right.push({ key: '0–9', text: 'Type a Time, such as 2130' });
    }
    right.push({ key: 'OK', text: f.row === formRows.length - 1 ? 'Record' : 'Next' });
    this.footer.set([], right);
  }

  /** wantGuide asks for the guide of the form's channel over its time, a moment after it last changed. */
  private wantGuide(): void {
    const self = this;
    clearTimeout(this.guideTimer);
    this.guideTimer = setTimeout(function () {
      const f = self.form;
      if (!f) {
        return;
      }
      const w = times(f);
      // The guide answers six hours at most: the first of a longer time.
      self.ctx.guide.want([f.channel.id], w.start, Math.min(w.stop, w.start + guideHours * 3600), function () {
        if (self.form === f) {
          self.draw();
        }
      });
    }, 600);
  }

  /** take puts what was typed on the row in focus into the form. */
  private take(): void {
    const f = this.form;
    clearTimeout(this.typedTimer);
    if (!f || !f.typed) {
      return;
    }
    const typed = f.typed;
    f.typed = '';
    if (f.row === 0) {
      const c = this.ctx.lineup.numbered(parseInt(typed, 10));
      if (c) {
        f.channel = c;
      } else {
        this.ctx.toast('There is no channel ' + typed + '.');
      }
      return;
    }
    const n = parseInt(typed, 10);
    const h = typed.length <= 2 ? n : Math.floor(n / 100);
    const m = typed.length <= 2 ? 0 : n % 100;
    if (h > 23 || m > 59) {
      this.ctx.toast(typed + ' is not a time. Type four digits, such as 2130.');
      return;
    }
    if (f.row === 2) {
      f.start = h * 60 + m;
    } else {
      f.end = h * 60 + m;
    }
  }

  /** typeDigit adds a digit typed on the channel's row or a time's, and takes them once there are enough. */
  private typeDigit(digit: number): void {
    const f = this.form!;
    if (f.row !== 0 && f.row !== 2 && f.row !== 3) {
      return;
    }
    f.typed += String(digit);
    const enough = f.row === 0 ? this.ctx.lineup.maxDigits : 4;
    const self = this;
    clearTimeout(this.typedTimer);
    if (f.typed.length >= enough) {
      this.take();
      this.wantGuide();
    } else {
      this.typedTimer = setTimeout(
        function () {
          if (self.form === f) {
            self.take();
            self.wantGuide();
            self.draw();
          }
        },
        f.row === 0 ? 1500 : 3000,
      );
    }
  }

  /** step changes the row in focus by delta: a channel, a day, or five minutes; or an hour, with far. */
  private step(delta: number, far: boolean): void {
    const f = this.form!;
    const day = 24 * 60;
    switch (f.row) {
      case 0: {
        const list = this.ctx.lineup.channels;
        let i = 0;
        for (let k = 0; k < list.length; k++) {
          if (list[k]!.id === f.channel.id) {
            i = k;
          }
        }
        const n = list.length;
        f.channel = list[(((i + delta * (far ? 10 : 1)) % n) + n) % n]!;
        break;
      }
      case 1:
        f.day = Math.max(0, Math.min(lastDay, f.day + delta));
        break;
      case 2:
      case 3: {
        const v = f.row === 2 ? f.start : f.end;
        const by = far ? 60 : 5;
        // Off the five minutes, a step goes to the next or the last of them.
        let next = delta > 0 ? Math.floor(v / by) * by + by : Math.ceil(v / by) * by - by;
        next = ((next % day) + day) % day;
        if (f.row === 2) {
          f.start = next;
        } else {
          f.end = next;
        }
        break;
      }
    }
    this.wantGuide();
  }

  /** submit asks the server to record as the form says. */
  private submit(): void {
    const f = this.form!;
    const ctx = this.ctx;
    const c = f.channel;
    const w = times(f);
    if (w.stop <= now()) {
      ctx.toast('That time has passed. Choose a later one.');
      return;
    }
    const self = this;
    withPIN(ctx, c, function (pin) {
      ctx.recordings.recordTimed(c.id, w.start, w.stop, pin, function (err, got) {
        if (err || !got) {
          const other = err && err.code === 'overlaps' ? ctx.recordings.forProgramme(c.id, w.start, w.stop) : null;
          ctx.toast("Couldn't record " + label(c) + '. ' + (other ? 'It is already recording ' + other.title + ' at that time.' : describe(err)));
          return;
        }
        if (got.start <= now()) {
          ctx.toast('Recording ' + got.title + ' until ' + clock(got.stop) + '. It will be on Movies.');
        } else {
          ctx.toast(got.title + ' will be recorded ' + onDay(got.start) + ' from ' + clock(got.start) + ' to ' + clock(got.stop) + '. It will be on Movies.');
        }
        if (self.form === f) {
          self.form = null;
          self.focusID = got.id;
          self.read();
          self.draw();
        }
      });
    });
  }

  private formKey(p: Press): boolean {
    const f = this.form!;
    switch (p.key) {
      case 'up':
        this.take();
        f.row = (f.row + formRows.length - 1) % formRows.length;
        break;
      case 'down':
        this.take();
        f.row = (f.row + 1) % formRows.length;
        break;
      case 'left':
      case 'right':
        this.take();
        this.step(p.key === 'left' ? -1 : 1, false);
        break;
      case 'pageUp':
      case 'pageDown':
      case 'channelPrev':
      case 'channelNext':
        this.take();
        this.step(p.key === 'pageDown' || p.key === 'channelPrev' ? -1 : 1, true);
        break;
      case 'digit':
        this.typeDigit(p.digit);
        break;
      case 'ok':
        this.take();
        if (f.row === formRows.length - 1) {
          this.submit();
        } else {
          f.row++;
        }
        break;
      case 'green':
      case 'record':
        this.take();
        this.submit();
        break;
      case 'back':
        clearTimeout(this.typedTimer);
        this.form = null;
        break;
      default:
        return false;
    }
    this.draw();
    return true;
  }

  key(p: Press): boolean {
    if (this.form) {
      return this.formKey(p);
    }
    switch (p.key) {
      case 'up':
        this.move(-1);
        break;
      case 'down':
        this.move(1);
        break;
      case 'pageUp':
      case 'channelPrev':
        this.move(-(shown - 1));
        break;
      case 'pageDown':
      case 'channelNext':
        this.move(shown - 1);
        break;
      case 'ok':
        this.open();
        return true;
      case 'green':
      case 'record':
        this.stopFocused();
        return true;
      default:
        return false;
    }
    this.draw();
    return true;
  }

  tick(): void {
    this.clockView.tick();
    if (!this.form && now() - this.readAt >= refreshEvery) {
      this.read();
    }
  }

  lineupChanged(): void {
    this.draw();
  }

  playback(): void {}

  video(): VideoArea {
    return 'none';
  }
}
