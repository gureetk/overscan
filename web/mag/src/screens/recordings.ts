// SPDX-License-Identifier: AGPL-3.0-or-later

// Recordings (D100), from Home while the server records: the household's
// recordings under way, those to come, and those of the last day that are
// done or failed. Green stops one under way, which keeps what it recorded,
// or calls off one to come; OK plays its channel, or opens Movies for one
// that is done.

import { el, setHTML, setText } from '../dom';
import { icon } from '../icons';
import type { Press } from '../keys';
import { label } from '../player';
import type { Recording } from '../recordings';
import { clock, date, day, now, span } from '../time';
import { stop } from './record';
import { Clock, type Ctx, escape, Footer, type Hint, type Screen, type VideoArea } from './screen';

type Section = 'now' | 'later' | 'ended';

/** A row of the list: a section's heading, or a recording. */
interface Row {
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

/** How many rows, headings with them, the list shows at once. */
const shown = 10;

/** How often the screen reads the list again while it shows, in seconds. */
const refreshEvery = 30;

/** size writes a file's size: 850 MB, or 1.4 GB. */
export function size(bytes: number): string {
  if (bytes >= 1e9) {
    return (Math.round(bytes / 1e8) / 10).toFixed(1) + ' GB';
  }
  return Math.max(1, Math.round(bytes / 1e6)) + ' MB';
}

export class RecordingsScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private rows: Row[] = [];
  /** The place in rows of the recording in focus, or -1 when there is none. */
  private focus = -1;
  /** The recording in focus, kept across a new list. */
  private focusID = 0;
  /** The first row shown. */
  private top = 0;
  private readAt = 0;

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
    el('span', 'page-title', head, 'Recordings');
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
    this.read();
    this.draw();
  }

  leave(): void {}

  /** read asks the server for the list again, and draws it once it has it. */
  private read(): void {
    const self = this;
    this.readAt = now();
    this.ctx.recordings.load(function () {
      self.draw();
    }, true);
  }

  /** build makes the rows from the list, and finds the recording in focus again. */
  private build(): void {
    const t = now();
    const recordings = this.ctx.recordings;
    const rows: Row[] = [];
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
    let at = -1;
    for (let i = 0; i < rows.length; i++) {
      if (rows[i]!.r && rows[i]!.r!.id === this.focusID) {
        at = i;
        break;
      }
    }
    if (at < 0) {
      // The recording in focus has gone: the next one from its place, else the one before.
      const from = Math.max(this.focus, 0);
      for (let i = from; i < rows.length && at < 0; i++) {
        if (rows[i]!.r) {
          at = i;
        }
      }
      for (let i = Math.min(from, rows.length - 1); i >= 0 && at < 0; i--) {
        if (rows[i]!.r) {
          at = i;
        }
      }
    }
    this.focus = at;
    this.focusID = at >= 0 ? rows[at]!.r!.id : 0;
  }

  /** move moves the focus by delta recordings, skipping headings. */
  private move(delta: number): void {
    if (this.focus < 0) {
      return;
    }
    const step = delta < 0 ? -1 : 1;
    let left = Math.abs(delta);
    let at = this.focus;
    for (let i = this.focus + step; i >= 0 && i < this.rows.length && left > 0; i += step) {
      if (this.rows[i]!.r) {
        at = i;
        left--;
      }
    }
    this.focus = at;
    this.focusID = this.rows[at]!.r!.id;
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
      if (row.head) {
        html += '<div class="setting-head' + (i > this.top ? ' gap' : '') + '">' + escape(row.head) + '</div>';
        continue;
      }
      const r = row.r!;
      html +=
        '<div class="setting' +
        (i === this.focus ? ' focus' : '') +
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
    this.drawInfo(this.focus >= 0 ? this.info(this.rows[this.focus]!) : this.empty());
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
      facts.push(['Ends', r.until_stopped ? 'When stopped, by ' + clock(r.stop) : clock(r.stop + 120)]);
      return {
        icon: 'record',
        title: r.title,
        text: r.until_stopped
          ? 'Recording until you stop it, or until ' + clock(r.stop) + ' at the latest. Green stops it, and what it recorded will be on Movies.'
          : 'Recording until two minutes after the programme ends. Green stops it sooner; either way, what it recorded will be on Movies.',
        facts: facts,
      };
    }
    if (row.section === 'later') {
      facts.push(['Day', date(r.start)]);
      facts.push(['Time', span(r.start, r.stop)]);
      return {
        icon: 'guide',
        title: r.title,
        text: 'Will be recorded from a minute before the programme to two minutes after it, then be on Movies. Green cancels it.',
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
    if (row && row.section !== 'ended' && this.ctx.lineup.channel(row.r!.channel)) {
      right.push({ key: 'OK', text: 'Watch the Channel' });
    } else if (row && row.r!.state === 'done') {
      right.push({ key: 'OK', text: 'Movies' });
    }
    if (this.count(0, this.rows.length) > 1) {
      right.push({ key: 'ud', text: 'Move' });
    }
    this.footer.set(left, right);
  }

  /** open plays the channel of a recording under way or to come, or opens Movies for one that is done. */
  private open(): void {
    const row = this.focus >= 0 ? this.rows[this.focus]! : null;
    if (!row) {
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
    if (!row || row.section === 'ended') {
      return;
    }
    const self = this;
    // The list read again keeps the focus on a stopped recording, which
    // moves to those that ended.
    stop(this.ctx, row.r!, function () {
      self.read();
    });
  }

  key(p: Press): boolean {
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
    if (now() - this.readAt >= refreshEvery) {
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
