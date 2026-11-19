// SPDX-License-Identifier: AGPL-3.0-or-later

// Radio: the radio stations that the sources list (D87), by group, with
// the station in focus beside them. OK plays a station, and OK again stops
// it; the station plays while the screen shows, with no picture.

import type { Channel } from '../api';
import { el, Logo, setHTML, setText, setWidth, show, toggle } from '../dom';
import { icon, star } from '../icons';
import type { Press } from '../keys';
import { type GroupView, indexOf } from '../lineup';
import { ListView } from '../list';
import { clock, duration, now, progress, span } from '../time';
import { Clock, type Ctx, escape, Footer, type Screen, type VideoArea } from './screen';

/** The rows the lists show: groups of 2.5625rem, stations of 4rem, as Live TV's. */
const groupRows = 14;
const stationRows = 8;

type Pane = 'groups' | 'stations';

interface StationRow {
  num: HTMLElement;
  logo: Logo;
  title: HTMLElement;
  lock: HTMLElement;
  time: HTMLElement;
  barBox: HTMLElement;
  bar: HTMLElement;
  fav: HTMLElement;
  sound: HTMLElement;
}

export class RadioScreen implements Screen {
  el: HTMLElement;
  tunes = true;
  private ctx: Ctx;
  private pane: Pane = 'stations';
  private groupKey = 'all';
  private groupList: ListView;
  private stationList: ListView;
  private stations: Channel[] = [];

  private groupName: HTMLElement;
  private groupCount: HTMLElement;
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
  private guideTimer = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    const self = this;
    this.ctx = ctx;
    this.el = el('div', 'screen live radio', parent);

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
        const g = self.groups()[i];
        if (g) {
          toggle(row, 'divider', i === 2);
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
    this.stationList = new ListView(
      listBox,
      stationRows,
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
    const frame = el('div', 'frame', side);
    const plate = el('div', 'plate', frame);
    this.plateLogo = new Logo('plate-logo', plate);
    this.plateHint = el('div', 'plate-hint', plate);
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

  private groups(): GroupView[] {
    return this.ctx.lineup.radioGroups;
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
    const sound = el('span', 'sound', marks);
    sound.innerHTML = icon('sound', 18, 2);
    const fav = el('span', 'fav', marks);
    fav.innerHTML = star(16);
    (row as any).parts = { num: num, logo: logo, title: title, lock: lock, time: time, barBox: barBox, bar: bar, fav: fav, sound: sound };
  }

  private drawRow(row: HTMLElement, i: number): void {
    const c = this.stations[i];
    const parts = (row as any).parts as StationRow;
    if (!c || !parts) {
      return;
    }
    const t = now();
    const p = this.ctx.guide.at(c.id, t);
    const on = this.playing(c);
    toggle(row, 'playing', on);
    setText(parts.num, String(c.number));
    parts.logo.set(c.logo || '', c.name);
    setText(parts.title, c.name);
    show(parts.lock, !!c.locked);
    setText(parts.time, p ? span(p.start, p.stop) + ' · ' + p.title : on ? 'Playing' : '');
    show(parts.barBox, !!p);
    if (p) {
      setWidth(parts.bar, progress(p.start, p.stop, t));
    }
    show(parts.sound, on);
    show(parts.fav, this.ctx.lineup.isFavourite(c.id));
  }

  /** playing says whether station c plays. */
  private playing(c: Channel): boolean {
    const playing = this.ctx.player.channel;
    return !!playing && playing.id === c.id;
  }

  enter(arg: any): void {
    if (!this.findGroup(this.groupKey)) {
      this.groupKey = 'all';
    }
    const focus: Channel | null = (arg && arg.channel) || this.ctx.player.channel;
    this.showGroup(this.groupKey, focus ? focus.id : 0);
    if (focus && indexOf(this.stations, focus.id) < 0) {
      this.showGroup('all', focus.id);
    }
    this.syncGroupList();
    this.setPane('stations');
    if (arg && arg.channel && arg.play) {
      this.play(arg.channel);
    }
  }

  leave(): void {
    clearTimeout(this.guideTimer);
    // A station plays while the screen shows.
    const c = this.ctx.player.channel;
    if (c && c.radio) {
      this.ctx.player.stop();
    }
  }

  private findGroup(key: string): GroupView | null {
    const groups = this.groups();
    for (let i = 0; i < groups.length; i++) {
      if (groups[i]!.key === key) {
        return groups[i]!;
      }
    }
    return null;
  }

  private syncGroupList(): void {
    const groups = this.groups();
    let gi = 0;
    for (let i = 0; i < groups.length; i++) {
      if (groups[i]!.key === this.groupKey) {
        gi = i;
      }
    }
    this.groupList.reset(groups.length, gi);
  }

  /** showGroup shows group key's stations, with the focus on station id if it is there. */
  private showGroup(key: string, id: number): void {
    const g = this.findGroup(key) || this.findGroup('all');
    if (!g) {
      return;
    }
    this.groupKey = g.key;
    this.stations = g.channels;
    const at = id ? indexOf(this.stations, id) : -1;
    this.stationList.reset(this.stations.length, Math.max(0, at));
    setText(this.groupName, g.name);
    const n = this.stations.length;
    setText(this.groupCount, n + (n === 1 ? ' station' : ' stations'));
    this.focusChanged();
  }

  private setPane(pane: Pane): void {
    this.pane = pane;
    this.groupList.setActive(pane === 'groups');
    this.stationList.setActive(pane === 'stations');
    this.drawFooter();
  }

  private focused(): Channel | null {
    return this.stations[this.stationList.focus()] || null;
  }

  /** focusChanged draws the side for the station in focus, and asks the guide for the rows. */
  private focusChanged(): void {
    const self = this;
    this.drawSide();
    clearTimeout(this.guideTimer);
    this.guideTimer = setTimeout(function () {
      const ids: number[] = [];
      const first = self.stationList.first();
      for (let i = first; i < first + stationRows && i < self.stations.length; i++) {
        ids.push(self.stations[i]!.id);
      }
      self.ctx.guide.wantNow(ids, function () {
        self.stationList.render();
        self.drawSide();
      });
    }, 150);
  }

  /** drawSide shows the station in focus, and which plays. */
  private drawSide(): void {
    const playing = this.ctx.player.channel;
    const c = this.focused();
    const t = now();
    if (!c) {
      this.plateLogo.set('', '');
      setText(this.plateHint, this.ctx.lineup.stations.length ? '' : 'No radio stations');
      setText(this.title, '');
      show(this.nowLine, false);
      show(this.barBox, false);
      setText(this.description, '');
      setHTML(this.nextList, '');
      return;
    }
    this.plateLogo.set(c.logo || '', c.name);
    let hint = c.locked ? 'Locked: OK asks for the PIN' : 'OK to listen';
    if (this.playing(c)) {
      hint = 'Playing · OK stops';
    } else if (playing) {
      hint += ' · Playing ' + playing.number + ' ' + playing.name;
    }
    setText(this.plateHint, hint);
    const p = this.ctx.guide.at(c.id, t);
    show(this.nowLine, !!p);
    show(this.barBox, !!p);
    if (p) {
      setText(this.nowText, span(p.start, p.stop) + ' · ' + duration(p.stop - t) + ' left');
      setText(this.title, p.title);
      setWidth(this.bar, progress(p.start, p.stop, t));
      setText(this.description, p.description || p.subtitle || '');
    } else {
      setText(this.title, c.name);
      setText(this.description, '');
    }
    const after = p ? p.stop : t;
    const next = this.ctx.guide.between(c.id, after, after + 6 * 3600);
    let html = '';
    let shown = 0;
    for (let i = 0; i < next.length && shown < 3; i++) {
      const q = next[i]!;
      if (q.start >= after) {
        html += '<div class="next"><span class="n-at">' + clock(q.start) + '</span><span class="n-title">' + escape(q.title) + '</span></div>';
        shown++;
      }
    }
    setHTML(this.nextList, html);
  }

  key(p: Press): boolean {
    const list = this.pane === 'groups' ? this.groupList : this.stationList;
    switch (p.key) {
      case 'up':
        list.move(-1);
        break;
      case 'down':
        list.move(1);
        break;
      case 'pageUp':
        list.page(-1);
        break;
      case 'pageDown':
        list.page(1);
        break;
      case 'channelPrev':
      case 'channelNext':
        if (this.ctx.player.channel) {
          this.zap(p.key === 'channelNext' ? 1 : -1);
          this.setPane('stations');
          break;
        }
        list.page(p.key === 'channelNext' ? 1 : -1);
        break;
      case 'left':
        this.setPane('groups');
        return true;
      case 'right':
        this.openFocusedGroup();
        return true;
      case 'back':
        if (this.pane === 'groups') {
          this.syncGroupList();
          this.setPane('stations');
          return true;
        }
        return false;
      case 'ok':
        if (this.pane === 'groups') {
          this.openFocusedGroup();
        } else {
          this.okOnStation();
        }
        return true;
      case 'red': {
        const c = this.focused();
        if (c && this.pane === 'stations') {
          this.ctx.toggleFavourite(c);
        }
        return true;
      }
      case 'tv':
        this.ctx.go('live');
        return true;
      default:
        return false;
    }
    if (this.pane === 'groups') {
      this.groupList.render();
    } else {
      this.focusChanged();
    }
    return true;
  }

  private openFocusedGroup(): void {
    const g = this.groups()[this.groupList.focus()];
    if (g && g.key !== this.groupKey) {
      this.showGroup(g.key, 0);
      this.groupList.render();
    }
    this.setPane('stations');
  }

  private okOnStation(): void {
    const c = this.focused();
    if (!c) {
      return;
    }
    if (this.playing(c)) {
      this.ctx.player.stop();
      this.playback();
      return;
    }
    this.play(c);
  }

  private play(c: Channel): void {
    this.ctx.watch(c, this.stations.length && indexOf(this.stations, c.id) >= 0 ? this.stations : [c], false, 'radio');
  }

  /** zap plays the station delta places along the list it was played from, and moves the focus to it. */
  private zap(delta: number): void {
    const player = this.ctx.player;
    const n = player.list.length;
    if (n === 0) {
      return;
    }
    const next = player.list[(((player.index + delta) % n) + n) % n]!;
    player.zap(delta);
    const at = indexOf(this.stations, next.id);
    if (at >= 0) {
      this.stationList.moveTo(at);
    }
  }

  private drawFooter(): void {
    const on = !!this.ctx.player.channel;
    this.footer.set(
      [{ key: 'red', text: 'Favourite' }],
      this.pane === 'groups'
        ? [
            { key: 'OK', text: 'Open' },
            { key: 'r', text: 'Stations' },
            { key: '0–9', text: 'Go to station' },
          ]
        : [
            { key: 'OK', text: on ? 'Listen or stop' : 'Listen' },
            { key: 'CH±', text: on ? 'Next station' : 'Page' },
            { key: 'l', text: 'Groups' },
          ],
    );
  }

  tune(c: Channel): void {
    if (indexOf(this.stations, c.id) < 0) {
      this.showGroup('all', c.id);
      this.syncGroupList();
    } else {
      this.stationList.moveTo(indexOf(this.stations, c.id));
      this.focusChanged();
    }
    this.setPane('stations');
    this.play(c);
  }

  numbering(): string {
    return 'radio';
  }

  tick(): void {
    this.clockView.tick();
    this.stationList.render();
    this.drawSide();
  }

  lineupChanged(): void {
    const c = this.focused();
    if (!this.findGroup(this.groupKey)) {
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
    this.stationList.render();
  }

  playback(): void {
    this.stationList.render();
    this.drawSide();
    this.drawFooter();
    this.ctx.videoChanged();
  }

  video(): VideoArea {
    return this.ctx.player.channel ? 'audio' : 'none';
  }
}
