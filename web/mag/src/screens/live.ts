// SPDX-License-Identifier: AGPL-3.0-or-later

// Live TV: the groups, a group's channels with what is on now, and beside
// them a small player with the programme of the channel in focus (D52).
// OK plays the channel in the small player; OK again goes full screen.

import type { Channel } from '../api';
import { el, Logo, setHTML, setText, setWidth, show, toggle } from '../dom';
import { icon, star } from '../icons';
import type { Press } from '../keys';
import { indexOf, isUHD } from '../lineup';
import { ListView } from '../list';
import { recordingNow } from '../recordings';
import { clock, duration, now, progress, span } from '../time';
import { record, recordable, recordText } from './record';
import { Clock, type Ctx, escape, Footer, type Screen, type VideoArea } from './screen';

/** The rows the lists show: groups of 2.5625rem, channels of 4rem. */
const groupRows = 14;
const channelRows = 8;
/** How long the focus rests on a channel before the page asks for its descriptions. */
const detailsDelayMs = 600;
/**
 * How long the focus rests on a group before its channels show: moving
 * through the groups redraws only the groups, which the box does quickly,
 * and not the channels with their logos.
 */
const groupDelayMs = 300;
/**
 * The channels of a group that the focus rests on show first without
 * their logos, and without asking what is on now; this long after, the
 * logos load and the guide is asked. A key in the groups before then
 * cancels it, so that moving through the groups at a medium pace costs
 * the box a few lines of text per group, not a screenful of images and a
 * request to the server.
 */
const fillDelayMs = 400;

type Pane = 'groups' | 'channels';

interface ChannelRow {
  num: HTMLElement;
  logo: Logo;
  title: HTMLElement;
  lock: HTMLElement;
  time: HTMLElement;
  barBox: HTMLElement;
  bar: HTMLElement;
  fav: HTMLElement;
  uhd: HTMLElement;
}

export class LiveScreen implements Screen {
  el: HTMLElement;
  tunes = true;
  private ctx: Ctx;
  private pane: Pane = 'channels';
  private groupKey = 'all';
  private groupList: ListView;
  private channelList: ListView;
  private channels: Channel[] = [];

  private groupName: HTMLElement;
  private groupCount: HTMLElement;
  private frame: HTMLElement;
  private plate: HTMLElement;
  private plateLogo: Logo;
  private plateHint: HTMLElement;
  private nowLine: HTMLElement;
  private nowText: HTMLElement;
  private title: HTMLElement;
  private barBox: HTMLElement;
  private bar: HTMLElement;
  private description: HTMLElement;
  private nextList: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private detailsTimer = 0;
  private guideTimer = 0;
  private groupTimer = 0;
  private fillTimer = 0;
  /** The channel rows show without logos, until fill. */
  private sparse = false;

  constructor(parent: HTMLElement, ctx: Ctx) {
    const self = this;
    this.ctx = ctx;
    this.el = el('div', 'screen live', parent);

    const groupsBox = el('div', 'groups', this.el);
    this.groupList = new ListView(
      groupsBox,
      groupRows,
      'group',
      function (row) {
        el('span', 'name', row);
        el('span', 'count', row);
      },
      function (row, i) {
        const g = ctx.lineup.groups[i];
        if (g) {
          toggle(row, 'divider', i === 3);
          toggle(row, 'shown', g.key === self.groupKey);
          setText(row.firstChild as HTMLElement, g.name);
          setText(row.lastChild as HTMLElement, String(g.channels.length));
        }
      },
    );

    const middle = el('div', 'channels', this.el);
    const head = el('div', 'list-head', middle);
    this.groupName = el('span', 'group-name', head);
    this.groupCount = el('span', 'group-count', head);
    const listBox = el('div', 'channel-list', middle);
    this.channelList = new ListView(
      listBox,
      channelRows,
      'channel',
      function (row) {
        self.makeRow(row);
      },
      function (row, i) {
        self.drawRow(row, i);
      },
    );

    const side = el('div', 'side', this.el);
    this.clockView = new Clock(side, true);
    this.frame = el('div', 'frame', side);
    this.plate = el('div', 'plate', this.frame);
    this.plateLogo = new Logo('plate-logo', this.plate);
    this.plateHint = el('div', 'plate-hint', this.plate);
    this.nowLine = el('div', 'now-line', side);
    el('span', 'live-dot', this.nowLine);
    el('span', 'now-word', this.nowLine, 'NOW');
    this.nowText = el('span', 'now-text', this.nowLine);
    this.title = el('div', 'now-title', side);
    this.barBox = el('div', 'bar', side);
    this.bar = el('div', 'fill acc', this.barBox);
    this.description = el('div', 'description', side);
    this.nextList = el('div', 'next-list', side);

    this.footer = new Footer(this.el);
  }

  private makeRow(row: HTMLElement): void {
    const num = el('span', 'num', row);
    const logo = new Logo('row-logo', row);
    const text = el('span', 'text', row);
    const line = el('span', 'line', text);
    const title = el('span', 'title', line);
    const lock = el('span', 'lock', line);
    lock.innerHTML = icon('lock', 14, 2);
    const meta = el('span', 'meta', text);
    const time = el('span', 'time', meta);
    const barBox = el('span', 'bar', meta);
    const bar = el('span', 'fill acc', barBox);
    const marks = el('span', 'marks', row);
    const fav = el('span', 'fav', marks);
    fav.innerHTML = star(16);
    const uhd = el('span', 'badge', marks, '4K');
    (row as any).parts = { num: num, logo: logo, title: title, lock: lock, time: time, barBox: barBox, bar: bar, fav: fav, uhd: uhd };
  }

  private drawRow(row: HTMLElement, i: number): void {
    const c = this.channels[i];
    const parts = (row as any).parts as ChannelRow;
    if (!c || !parts) {
      return;
    }
    const t = now();
    const p = this.ctx.guide.at(c.id, t);
    const playing = this.ctx.player.channel;
    toggle(row, 'playing', !!playing && playing.id === c.id);
    setText(parts.num, String(this.ctx.lineup.shownNumber(c, this.groupKey, i)));
    parts.logo.set(this.sparse ? '' : c.logo || '', c.name);
    setText(parts.title, p ? p.title : c.name);
    show(parts.lock, !!c.locked);
    setText(parts.time, p ? span(p.start, p.stop) : p === null && this.ctx.guide.knows(c.id, t, t + 1) ? 'No guide' : '');
    show(parts.barBox, !!p);
    if (p) {
      setWidth(parts.bar, progress(p.start, p.stop, t));
    }
    show(parts.fav, this.ctx.lineup.isFavourite(c.id));
    show(parts.uhd, isUHD(c));
  }

  enter(arg: any): void {
    const playing = this.ctx.player.channel;
    if (!this.ctx.lineup.group(this.groupKey)) {
      this.groupKey = 'all';
    }
    this.pane = 'channels';
    const focus: Channel | null = (arg && arg.channel) || playing;
    this.showGroup(this.groupKey, focus ? focus.id : 0);
    if (focus && indexOf(this.channels, focus.id) < 0) {
      this.showGroup('all', focus.id);
    }
    this.syncGroupList();
    this.setPane('channels');
    this.drawFooter();
    const self = this;
    this.ctx.recordings.load(function () {
      self.drawSide();
    });
  }

  leave(): void {
    clearTimeout(this.detailsTimer);
    clearTimeout(this.guideTimer);
    clearTimeout(this.groupTimer);
    clearTimeout(this.fillTimer);
  }

  /**
   * showFocusedGroup shows the channels of the group in focus: sparsely
   * while the focus is still in the groups, in full when it moves to the
   * channels.
   */
  private showFocusedGroup(sparse: boolean): void {
    clearTimeout(this.groupTimer);
    const g = this.ctx.lineup.groups[this.groupList.focus()];
    if (g && g.key !== this.groupKey) {
      this.showGroup(g.key, 0, sparse);
      this.groupList.render();
    } else if (sparse) {
      this.fillLater();
    } else {
      this.fill();
    }
  }

  /** fill draws the logos that the rows went without, and asks what is on now. */
  private fill(): void {
    clearTimeout(this.fillTimer);
    if (this.sparse) {
      this.sparse = false;
      this.channelList.render();
      this.focusChanged();
    }
  }

  /** fillLater fills the rows once the focus has rested a while longer. */
  private fillLater(): void {
    const self = this;
    clearTimeout(this.fillTimer);
    if (this.sparse) {
      this.fillTimer = setTimeout(function () {
        self.fill();
      }, fillDelayMs);
    }
  }

  private syncGroupList(): void {
    const groups = this.ctx.lineup.groups;
    let gi = 0;
    for (let i = 0; i < groups.length; i++) {
      if (groups[i]!.key === this.groupKey) {
        gi = i;
      }
    }
    this.groupList.reset(groups.length, gi);
  }

  /**
   * showGroup shows group key's channels, with the focus on channel id if
   * it is there; sparse, it leaves the logos and the guide for fill.
   */
  private showGroup(key: string, id: number, sparse?: boolean): void {
    const g = this.ctx.lineup.group(key) || this.ctx.lineup.group('all');
    if (!g) {
      return;
    }
    clearTimeout(this.fillTimer);
    this.sparse = !!sparse;
    this.groupKey = g.key;
    this.channels = this.ctx.lineup.list(g);
    const at = id ? indexOf(this.channels, id) : -1;
    this.channelList.reset(this.channels.length, Math.max(0, at));
    setText(this.groupName, g.name);
    const n = this.channels.length;
    setText(this.groupCount, n + (n === 1 ? ' channel' : ' channels'));
    if (!this.sparse) {
      this.focusChanged();
      return;
    }
    clearTimeout(this.guideTimer);
    clearTimeout(this.detailsTimer);
    this.drawSide();
    this.fillLater();
  }

  private setPane(pane: Pane): void {
    this.pane = pane;
    this.groupList.setActive(pane === 'groups');
    this.channelList.setActive(pane === 'channels');
    this.drawFooter();
  }

  private focused(): Channel | null {
    return this.channels[this.channelList.focus()] || null;
  }

  /** focusChanged draws the side for the channel in focus, and asks for what the rows need. */
  private focusChanged(): void {
    const self = this;
    this.drawSide();
    clearTimeout(this.guideTimer);
    this.guideTimer = setTimeout(function () {
      const ids: number[] = [];
      const first = self.channelList.first();
      for (let i = first; i < first + channelRows && i < self.channels.length; i++) {
        ids.push(self.channels[i]!.id);
      }
      self.ctx.guide.wantNow(ids, function () {
        self.channelList.render();
        self.drawSide();
      });
    }, 150);
    clearTimeout(this.detailsTimer);
    const c = this.focused();
    if (c) {
      this.detailsTimer = setTimeout(function () {
        self.ctx.guide.wantDetails(c.id, function () {
          if (self.focused() === c) {
            self.drawSide();
          }
        });
      }, detailsDelayMs);
    }
  }

  private drawSide(): void {
    this.drawFooter();
    const c = this.focused();
    const t = now();
    const playing = this.ctx.player.channel;
    // A radio station has no picture: its logo shows in its place (D87).
    show(this.plate, !playing || !!playing.radio);
    if (!c) {
      setText(this.title, '');
      show(this.nowLine, false);
      show(this.barBox, false);
      setText(this.description, '');
      setHTML(this.nextList, '');
      return;
    }
    const station = playing && playing.radio ? playing : null;
    if (station) {
      this.plateLogo.set(station.logo || '', station.name);
      setText(this.plateHint, 'Radio · ' + station.number + ' ' + station.name);
    } else {
      this.plateLogo.set(this.sparse ? '' : c.logo || '', c.name);
      setText(this.plateHint, c.locked ? 'Locked: OK asks for the PIN' : 'OK to watch here');
    }
    const details = this.ctx.guide.detailsOf(c.id);
    let p = this.ctx.guide.at(c.id, t);
    if (p && details) {
      for (let i = 0; i < details.length; i++) {
        if (details[i]!.id === p.id) {
          p = details[i]!;
        }
      }
    }
    show(this.nowLine, !!p);
    show(this.barBox, !!p);
    if (p) {
      const rec = this.ctx.recordings.find(c.id, p.start);
      const recText = rec ? (recordingNow(rec, t) ? ' · Recording' : ' · To be recorded') : '';
      setText(this.nowText, span(p.start, p.stop) + ' · ' + duration(p.stop - t) + ' left' + recText);
      setText(this.title, p.title);
      setWidth(this.bar, progress(p.start, p.stop, t));
      setText(this.description, p.description || p.subtitle || '');
    } else {
      setText(this.title, c.name);
      setText(this.description, this.ctx.guide.knows(c.id, t, t + 1) ? 'No guide for this channel.' : '');
    }
    // What comes next: from the descriptions if the page has them, else the guide.
    const after = p ? p.stop : t;
    const source = details || this.ctx.guide.between(c.id, after, after + 6 * 3600);
    let html = '';
    let shown = 0;
    for (let i = 0; i < source.length && shown < 3; i++) {
      const q = source[i]!;
      if (q.start >= after) {
        html += '<div class="next"><span class="n-at">' + clock(q.start) + '</span><span class="n-title">' + escape(q.title) + '</span></div>';
        shown++;
      }
    }
    setHTML(this.nextList, html);
    toggle(this.el, 'with-video', !!playing && !playing.radio);
  }

  key(p: Press): boolean {
    const list = this.pane === 'groups' ? this.groupList : this.channelList;
    switch (p.key) {
      case 'up':
        list.move(-1);
        break;
      case 'down':
        list.move(1);
        break;
      case 'pageUp':
      case 'channelPrev':
        list.page(-1);
        break;
      case 'pageDown':
      case 'channelNext':
        list.page(1);
        break;
      case 'left':
        this.setPane('groups');
        return true;
      case 'right':
        this.showFocusedGroup(false);
        this.setPane('channels');
        return true;
      case 'back':
        if (this.pane === 'groups') {
          if (this.previews()) {
            this.showFocusedGroup(false);
          } else {
            // Back opens nothing: the focus goes back to the group shown.
            this.syncGroupList();
          }
          this.setPane('channels');
          return true;
        }
        return false;
      case 'ok':
        if (this.pane === 'groups') {
          this.showFocusedGroup(false);
          this.setPane('channels');
        } else {
          this.okOnChannel();
        }
        return true;
      case 'red': {
        const c = this.focused();
        if (c && this.pane === 'channels') {
          this.ctx.toggleFavourite(c);
        }
        return true;
      }
      case 'green':
      case 'record':
        this.recordNow();
        return true;
      case 'yellow':
        this.sort();
        return true;
      case 'blue':
        this.ctx.go('search');
        return true;
      default:
        return false;
    }
    if (this.pane === 'groups') {
      const self = this;
      clearTimeout(this.groupTimer);
      clearTimeout(this.fillTimer);
      if (this.previews()) {
        this.groupTimer = setTimeout(function () {
          self.showFocusedGroup(true);
        }, groupDelayMs);
      }
    } else {
      this.focusChanged();
    }
    return true;
  }

  /**
   * previews reports whether the channels follow the focus through the
   * groups, or wait for OK or Right, as the box's settings say (D62).
   */
  private previews(): boolean {
    return this.ctx.settings.current.groups !== 'ok';
  }

  private okOnChannel(): void {
    const c = this.focused();
    if (!c) {
      return;
    }
    const playing = this.ctx.player.channel;
    if (playing && playing.id === c.id) {
      if (c.radio) {
        this.ctx.go('radio', { channel: c });
      } else {
        this.ctx.go('watch');
      }
    } else {
      this.ctx.watch(c, this.channels, false, this.groupKey);
    }
  }

  private sort(): void {
    const c = this.focused();
    this.ctx.lineup.setByName(!this.ctx.lineup.byName);
    this.ctx.toast(this.ctx.lineup.byName ? 'Channels are now sorted by name.' : 'Channels are now sorted by number.');
    this.showGroup(this.groupKey, c ? c.id : 0);
    this.drawFooter();
  }

  /** recordNow records what is on now on the channel in focus, or calls its recording off. */
  private recordNow(): void {
    const self = this;
    const c = this.focused();
    if (c && this.pane === 'channels') {
      record(this.ctx, c, this.ctx.guide.at(c.id, now()), function () {
        self.drawSide();
      });
    }
  }

  private drawFooter(): void {
    const colours = [{ key: 'red', text: 'Favourite' }];
    const c = this.focused();
    const p = c ? this.ctx.guide.at(c.id, now()) : null;
    if (c && p && this.pane === 'channels' && recordable(this.ctx, p)) {
      colours.push({ key: 'green', text: recordText(this.ctx, c, p) });
    }
    colours.push({ key: 'yellow', text: this.ctx.lineup.byName ? 'Sort by Number' : 'Sort by Name' }, { key: 'blue', text: 'Search' });
    this.footer.set(
      colours,
      this.pane === 'groups'
        ? [
            { key: 'OK', text: 'Open' },
            { key: 'r', text: 'Channels' },
            { key: '0–9', text: 'Go to channel' },
          ]
        : [
            { key: 'OK', text: 'Watch' },
            { key: 'l', text: 'Groups' },
            { key: '0–9', text: 'Go to channel' },
          ],
    );
  }

  tune(c: Channel): void {
    this.fill();
    if (indexOf(this.channels, c.id) < 0) {
      this.showGroup('all', c.id);
      this.syncGroupList();
    } else {
      this.channelList.moveTo(indexOf(this.channels, c.id));
      this.focusChanged();
    }
    this.setPane('channels');
    this.ctx.watch(c, this.channels, false, this.groupKey);
  }

  numbering(): string {
    return this.groupKey;
  }

  tick(): void {
    this.clockView.tick();
    this.channelList.render();
    this.drawSide();
  }

  lineupChanged(): void {
    const c = this.focused();
    if (!this.ctx.lineup.group(this.groupKey)) {
      this.groupKey = 'all';
    }
    this.showGroup(this.groupKey, c ? c.id : 0);
    this.syncGroupList();
    this.setPane(this.pane);
  }

  /** favouritesChanged redraws after a favourite is added or removed. */
  favouritesChanged(): void {
    if (this.groupKey === 'fav') {
      const c = this.focused();
      this.showGroup('fav', c ? c.id : 0);
    }
    this.groupList.render();
    this.channelList.render();
  }

  playback(): void {
    this.channelList.render();
    this.drawSide();
    this.ctx.videoChanged();
  }

  video(): VideoArea {
    const playing = this.ctx.player.channel;
    if (!playing) {
      return 'none';
    }
    if (playing.radio) {
      return 'audio';
    }
    // The frame's place in the 1280×720 layout, in the page's pixels.
    const unit = parseFloat(document.documentElement.style.fontSize) || 16;
    return { x: 50 * unit, y: 5.25 * unit, w: 26 * unit, h: 14.625 * unit };
  }
}
