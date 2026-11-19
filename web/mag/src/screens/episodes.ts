// SPDX-License-Identifier: AGPL-3.0-or-later

// A series' episodes: its seasons, a season's episodes with how far the
// account got into each, and beside them the episode in focus. OK plays it,
// going on from where the account got to (D65).

import type { EpisodeInfo, Season, Title } from '../api';
import { el, Logo, setText, setWidth, show, toggle } from '../dom';
import type { Press } from '../keys';
import { ListView } from '../list';
import { duration, hms } from '../time';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';
import { findEpisode } from './title';

const seasonRows = 14;
const episodeRows = 8;
/** How long the focus rests on an episode before its picture loads. */
const pictureDelayMs = 250;

type Pane = 'seasons' | 'episodes';

interface EpisodeParts {
  num: HTMLElement;
  name: HTMLElement;
  meta: HTMLElement;
  barBox: HTMLElement;
  bar: HTMLElement;
  check: HTMLElement;
}

/** EpisodesArg is what go('episodes') takes: the series, and the episode to put the focus on. */
export interface EpisodesArg {
  title: Title;
  episode: number;
}

export class EpisodesScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private title: Title | null = null;
  private pane: Pane = 'episodes';
  private seasonAt = 0;
  private seasonList: ListView;
  private episodeList: ListView;
  private seriesName: HTMLElement;
  private seasonName: HTMLElement;
  private picture: Logo;
  private sideName: HTMLElement;
  private sideMeta: HTMLElement;
  private sidePlot: HTMLElement;
  private sideNote: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private pictureTimer = 0;
  private seq = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    const self = this;
    this.ctx = ctx;
    this.el = el('div', 'screen live episodes', parent);
    const seasonsBox = el('div', 'groups', this.el);
    this.seriesName = el('div', 'series-name', seasonsBox);
    this.seasonList = new ListView(
      seasonsBox,
      seasonRows,
      'group',
      function (row) {
        el('span', 'name', row);
        el('span', 'count', row);
      },
      function (row, i) {
        const s = self.seasons()[i];
        if (s) {
          toggle(row, 'shown', i === self.seasonAt);
          setText(row.firstChild as HTMLElement, s.name);
          setText(row.lastChild as HTMLElement, String(s.episodes.length));
        }
      },
    );
    const middle = el('div', 'channels', this.el);
    const head = el('div', 'list-head', middle);
    this.seasonName = el('span', 'group-name', head);
    const listBox = el('div', 'channel-list', middle);
    this.episodeList = new ListView(
      listBox,
      episodeRows,
      'channel episode-row',
      function (row) {
        const num = el('span', 'num', row);
        const text = el('span', 'text', row);
        const name = el('span', 'line', text);
        const sub = el('span', 'meta', text);
        const m = el('span', 'time', sub);
        const barBox = el('span', 'bar', sub);
        const bar = el('span', 'fill acc', barBox);
        const check = el('span', 'badge watched', row, 'Watched');
        (row as any).parts = { num: num, name: name, meta: m, barBox: barBox, bar: bar, check: check };
      },
      function (row, i) {
        self.drawRow(row, i);
      },
    );
    const side = el('div', 'side', this.el);
    this.clockView = new Clock(side, true);
    const frame = el('div', 'frame still-frame', side);
    this.picture = new Logo('side-still', frame);
    this.sideName = el('div', 'now-title', side);
    this.sideMeta = el('div', 'side-meta', side);
    this.sidePlot = el('div', 'description', side);
    this.sideNote = el('div', 'side-note', side);
    this.footer = new Footer(this.el);
  }

  private seasons(): Season[] {
    return (this.title && this.title.seasons) || [];
  }

  private episodes(): EpisodeInfo[] {
    const s = this.seasons()[this.seasonAt];
    return s ? s.episodes : [];
  }

  private drawRow(row: HTMLElement, i: number): void {
    const e = this.episodes()[i];
    const parts = (row as any).parts as EpisodeParts;
    if (!e || !parts) {
      return;
    }
    setText(parts.num, String(e.number));
    setText(parts.name, e.name || 'Episode ' + e.number);
    const p = e.progress;
    const left = p && !p.finished && p.position > 0 && p.duration > 0 ? duration(p.duration - p.position) + ' left' : '';
    setText(parts.meta, left || (e.duration ? duration(e.duration) : ''));
    const part = p && !p.finished && p.position > 0 && p.duration > 0;
    show(parts.barBox, !!part);
    if (part) {
      setWidth(parts.bar, (p!.position / p!.duration) * 100);
    }
    show(parts.check, !!(p && p.finished));
  }

  enter(arg: any): void {
    const a = arg as EpisodesArg | undefined;
    if (a && a.title) {
      this.showSeries(a.title, a.episode);
      this.setPane('episodes');
      return;
    }
    // Back from an episode: ask for the series again, for its progress.
    this.refresh();
  }

  leave(): void {
    clearTimeout(this.pictureTimer);
    this.seq++;
  }

  /** showSeries shows a series' seasons, with the focus on an episode. */
  private showSeries(t: Title, episodeID: number): void {
    this.title = t;
    setText(this.seriesName, t.name);
    const found = episodeID ? findEpisode(t, episodeID) : null;
    const seasons = this.seasons();
    this.seasonAt = 0;
    for (let i = 0; found && i < seasons.length; i++) {
      if (seasons[i]!.number === found.season) {
        this.seasonAt = i;
      }
    }
    this.seasonList.reset(seasons.length, this.seasonAt);
    this.showSeason(found ? this.episodes().indexOf(found.episode) : 0);
  }

  private refresh(): void {
    const self = this;
    const t = this.title;
    if (!t) {
      return;
    }
    const focus = this.episodes()[this.episodeList.focus()];
    const seq = ++this.seq;
    this.ctx.series.open(t.id, function (err, fresh) {
      if (seq !== self.seq || err || !fresh) {
        return;
      }
      const pane = self.pane;
      self.showSeries(fresh, focus ? focus.id : 0);
      self.setPane(pane);
    });
  }

  private showSeason(focus: number): void {
    const s = this.seasons()[this.seasonAt];
    setText(this.seasonName, s ? s.name : '');
    this.seasonList.render();
    this.episodeList.reset(this.episodes().length, Math.max(0, focus));
    this.focusChanged();
  }

  private setPane(pane: Pane): void {
    this.pane = pane;
    this.seasonList.setActive(pane === 'seasons');
    this.episodeList.setActive(pane === 'episodes');
    this.drawFooter();
  }

  private focused(): EpisodeInfo | null {
    return this.episodes()[this.episodeList.focus()] || null;
  }

  private focusChanged(): void {
    const self = this;
    const e = this.focused();
    const s = this.seasons()[this.seasonAt];
    clearTimeout(this.pictureTimer);
    this.pictureTimer = setTimeout(function () {
      const f = self.focused();
      self.picture.set(f ? f.picture || '' : '', f ? f.name || '' : '');
    }, pictureDelayMs);
    if (!e || !s) {
      setText(this.sideName, '');
      setText(this.sideMeta, '');
      setText(this.sidePlot, '');
      setText(this.sideNote, '');
      return;
    }
    setText(this.sideName, e.name || 'Episode ' + e.number);
    setText(this.sideMeta, 'S' + s.number + ' E' + e.number + (e.duration ? ' · ' + duration(e.duration) : ''));
    setText(this.sidePlot, e.plot || '');
    const p = e.progress;
    setText(
      this.sideNote,
      p && !p.finished && p.position > 0 ? 'OK to go on from ' + hms(p.position) + '.' : p && p.finished ? 'Watched. OK to play it again.' : 'OK to play.',
    );
  }

  key(p: Press): boolean {
    const list = this.pane === 'seasons' ? this.seasonList : this.episodeList;
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
        this.setPane('seasons');
        return true;
      case 'right':
        this.openSeason();
        this.setPane('episodes');
        return true;
      case 'back':
        if (this.pane === 'episodes' && this.seasons().length > 1) {
          this.setPane('seasons');
          return true;
        }
        return false;
      case 'ok':
        if (this.pane === 'seasons') {
          this.openSeason();
          this.setPane('episodes');
        } else {
          this.play();
        }
        return true;
      default:
        return false;
    }
    if (this.pane === 'seasons') {
      this.openSeason();
    } else {
      this.focusChanged();
    }
    return true;
  }

  /** openSeason shows the season in focus, which is no more than a list the page has. */
  private openSeason(): void {
    const at = this.seasonList.focus();
    if (at !== this.seasonAt) {
      this.seasonAt = at;
      this.showSeason(0);
    }
  }

  private play(): void {
    const t = this.title;
    const e = this.focused();
    const s = this.seasons()[this.seasonAt];
    if (!t || !e || !s) {
      return;
    }
    const p = e.progress;
    this.ctx.playFilm({ title: t, episode: e, season: s.number }, p && !p.finished ? p.position : 0);
  }

  private drawFooter(): void {
    this.footer.set(
      [],
      this.pane === 'seasons'
        ? [
            { key: 'OK', text: 'Open' },
            { key: 'r', text: 'Episodes' },
          ]
        : [
            { key: 'OK', text: 'Play' },
            { key: 'l', text: 'Seasons' },
          ],
    );
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
