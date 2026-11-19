// SPDX-License-Identifier: AGPL-3.0-or-later

// Films and series: the shelves, a shelf's titles, and beside them the
// poster of the title in focus (D63). OK opens a title. A source's films or
// series are read the first time a box asks, so the screen can say that
// they are still loading, and asks again.

import { describe, type LibraryState, type Shelf, type ShelfPage, type TitleKind, type TitleRow } from '../api';
import { el, Logo, setText, setWidth, show, toggle } from '../dom';
import { icon } from '../icons';
import type { Press } from '../keys';
import { hiddenPoster, type LibraryModel, thumb } from '../library';
import { ListView } from '../list';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';

const shelfRows = 14;
const titleRows = 8;
/** How long the focus rests on a shelf before its titles show, when it previews. */
const shelfDelayMs = 450;
/** How long the focus rests on a title before its poster loads. */
const posterDelayMs = 250;
/** How long a shelf previewed without pictures waits before its pictures load. */
const fillDelayMs = 400;
/** How often the screen asks again while the library loads. */
const pollMs = 2500;
/** When the focus comes this near the end of what is loaded, the next part loads. */
const loadAhead = 40;

type Pane = 'shelves' | 'titles';

interface TitleParts {
  thumb: Logo;
  lock: HTMLElement;
  name: HTMLElement;
  meta: HTMLElement;
  barBox: HTMLElement;
  bar: HTMLElement;
  check: HTMLElement;
}

/** meta writes a title's year and rating: 2024 · 7.4. */
export function meta(t: { year?: number; rating?: number }): string {
  const parts: string[] = [];
  if (t.year) {
    parts.push(String(t.year));
  }
  if (t.rating) {
    parts.push('★ ' + (t.rating / 10).toFixed(1));
  }
  return parts.join(' · ');
}

export class LibraryScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private model: LibraryModel;
  private kind: TitleKind;
  private noun: string;
  private pane: Pane = 'shelves';
  private shelves: Shelf[] = [];
  /** The shelf shown, its order, and its titles so far. */
  private shelfId = '';
  private order = 'added';
  private titles: TitleRow[] = [];
  /** Where the shelf's next part starts, -1 after the last. */
  private next = -1;
  private loadingMore = false;
  private seq = 0;
  private sparse = false;

  private shelfList: ListView;
  private titleList: ListView;
  private shelfName: HTMLElement;
  private shelfCount: HTMLElement;
  private status: HTMLElement;
  private poster: Logo;
  private sideName: HTMLElement;
  private sideMeta: HTMLElement;
  private sideNote: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private pollTimer = 0;
  private shelfTimer = 0;
  private posterTimer = 0;
  private fillTimer = 0;

  constructor(parent: HTMLElement, ctx: Ctx, model: LibraryModel) {
    const self = this;
    this.ctx = ctx;
    this.model = model;
    this.kind = model.kind;
    this.noun = this.kind === 'movie' ? 'films' : 'series';
    this.el = el('div', 'screen live library', parent);

    const shelvesBox = el('div', 'groups', this.el);
    this.shelfList = new ListView(
      shelvesBox,
      shelfRows,
      'group',
      function (row) {
        el('span', 'name', row);
        el('span', 'count', row);
      },
      function (row, i) {
        const s = self.shelves[i];
        if (s) {
          const special = s.id === 'continue' || s.id === 'recent';
          const next = self.shelves[i - 1];
          toggle(row, 'divider', !special && !!next && (next.id === 'continue' || next.id === 'recent'));
          toggle(row, 'shown', s.id === self.shelfId);
          setText(row.firstChild as HTMLElement, s.name);
          setText(row.lastChild as HTMLElement, s.count < 0 ? '' : String(s.count));
        }
      },
    );

    const middle = el('div', 'channels', this.el);
    const head = el('div', 'list-head', middle);
    this.shelfName = el('span', 'group-name', head);
    this.shelfCount = el('span', 'group-count', head);
    this.status = el('div', 'library-status', middle);
    const listBox = el('div', 'channel-list', middle);
    this.titleList = new ListView(
      listBox,
      titleRows,
      'channel title-row',
      function (row) {
        const t = new Logo('row-thumb', row);
        const text = el('span', 'text', row);
        const name = el('span', 'line', text);
        const sub = el('span', 'meta', text);
        const m = el('span', 'time', sub);
        const barBox = el('span', 'bar', sub);
        const bar = el('span', 'fill acc', barBox);
        const check = el('span', 'badge watched', row, 'Watched');
        const lock = el('span', 'lock-mark', row);
        lock.innerHTML = icon('lock', 16, 2);
        (row as any).parts = { thumb: t, lock: lock, name: name, meta: m, barBox: barBox, bar: bar, check: check };
      },
      function (row, i) {
        self.drawRow(row, i);
      },
    );

    const side = el('div', 'side', this.el);
    this.clockView = new Clock(side, true);
    const frame = el('div', 'poster-frame', side);
    this.poster = new Logo('side-poster', frame);
    this.sideName = el('div', 'now-title', side);
    this.sideMeta = el('div', 'side-meta', side);
    this.sideNote = el('div', 'description', side);
    this.footer = new Footer(this.el);
  }

  private drawRow(row: HTMLElement, i: number): void {
    const t = this.titles[i];
    const parts = (row as any).parts as TitleParts;
    if (!t || !parts) {
      return;
    }
    const hidden = hiddenPoster(t);
    parts.thumb.set(this.sparse || hidden ? '' : thumb(t.poster), ''); // the name is beside it
    show(parts.lock, !!t.locked);
    setText(parts.name, t.name);
    setText(parts.meta, t.episode ? t.episode + (t.year ? ' · ' + t.year : '') : meta(t));
    show(parts.barBox, !!t.progress);
    if (t.progress) {
      setWidth(parts.bar, t.progress);
    }
    show(parts.check, !!t.watched);
  }

  enter(): void {
    // What the account watched may have changed since the screen last
    // showed: the shelves and the shelf open are asked for again, keeping
    // the focus.
    this.model.forget();
    if (this.shelves.length) {
      this.reloadShelf();
    } else {
      this.load();
    }
  }

  leave(): void {
    clearTimeout(this.pollTimer);
    clearTimeout(this.shelfTimer);
    clearTimeout(this.posterTimer);
    clearTimeout(this.fillTimer);
  }

  /** fill draws the pictures that a shelf previewed without them went without. */
  private fill(): void {
    clearTimeout(this.fillTimer);
    if (this.sparse) {
      this.sparse = false;
      this.titleList.render();
      this.focusChanged();
    }
  }

  /** load asks for the shelves, and again in a moment while they load. */
  private load(): void {
    const self = this;
    clearTimeout(this.pollTimer);
    const seq = ++this.seq;
    if (!this.shelves.length) {
      this.showStatus('Loading the ' + this.noun + '…');
    }
    this.model.state(function (err, st) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !st) {
        if (err && (err.code === 'unauthorized' || err.code === 'blocked')) {
          return;
        }
        self.showStatus("Couldn't load the " + self.noun + '. ' + describe(err));
        self.pollTimer = setTimeout(function () {
          self.load();
        }, pollMs * 4);
        return;
      }
      self.loaded(st);
    });
  }

  private loaded(st: LibraryState): void {
    const self = this;
    switch (st.state) {
      case 'none':
        this.shelves = [];
        this.shelfList.reset(0, 0);
        this.showStatus('No source has ' + this.noun + '. An Xtream, a Jellyfin or a Plex server, or a folder, added in the admin brings its ' + this.noun + ' here.');
        return;
      case 'loading':
        this.showStatus('Reading the ' + this.noun + ' from your source. The first time takes a moment…');
        this.pollTimer = setTimeout(function () {
          self.load();
        }, pollMs);
        return;
      case 'failed':
        this.showStatus("Couldn't read the " + this.noun + ' from your source: ' + (st.error || 'it gave no reason') + '. Overscan tries again in a few minutes.');
        this.pollTimer = setTimeout(function () {
          self.load();
        }, pollMs * 24);
        return;
    }
    const was = this.shelfId;
    this.shelves = st.shelves;
    if (!this.shelves.length) {
      this.shelfList.reset(0, 0);
      this.showStatus('Your source lists no ' + this.noun + '.');
      return;
    }
    let at = 0;
    for (let i = 0; i < this.shelves.length; i++) {
      if (this.shelves[i]!.id === was) {
        at = i;
      }
    }
    this.shelfList.reset(this.shelves.length, at);
    this.openShelf(this.shelves[at]!.id, false);
    this.setPane(was ? this.pane : 'shelves');
  }

  private showStatus(text: string): void {
    setText(this.status, text);
    show(this.status, true);
    setText(this.shelfName, this.kind === 'movie' ? 'Movies' : 'Series');
    setText(this.shelfCount, '');
    this.titles = [];
    this.titleList.reset(0, 0);
    this.drawSide();
    this.drawFooter();
  }

  /** openShelf shows a shelf's titles; sparse leaves their pictures for later. */
  private openShelf(id: string, sparse: boolean): void {
    const self = this;
    const s = this.shelf(id);
    if (!s) {
      return;
    }
    clearTimeout(this.shelfTimer);
    const keep = id === this.shelfId ? this.titles[this.titleList.focus()] : null;
    this.shelfId = id;
    this.sparse = sparse;
    this.shelfList.render();
    setText(this.shelfName, s.name);
    setText(this.shelfCount, countText(s.count));
    const seq = ++this.seq;
    this.model.shelf(id, this.orderOf(id), 0, function (err, page) {
      if (seq !== self.seq) {
        return;
      }
      if (err || !page) {
        self.showStatus("Couldn't load " + s.name + '. ' + describe(err));
        return;
      }
      show(self.status, false);
      self.titles = page.titles;
      self.next = nextFrom(page);
      let at = 0;
      for (let i = 0; keep && i < self.titles.length; i++) {
        if (self.titles[i]!.id === keep.id) {
          at = i;
        }
      }
      self.titleList.reset(self.titles.length, at);
      setText(self.shelfCount, countText(page.total));
      self.focusChanged();
      self.drawFooter();
      clearTimeout(self.fillTimer);
      if (self.sparse) {
        self.fillTimer = setTimeout(function () {
          self.fill();
        }, fillDelayMs);
      }
    });
  }

  /** reloadShelf shows the shelf again, keeping the focus, after the account watched something. */
  private reloadShelf(): void {
    const self = this;
    const seq = ++this.seq;
    this.model.state(function (err, st) {
      if (seq !== self.seq || err || !st) {
        return;
      }
      if (st.state !== 'ready') {
        self.shelves = [];
        self.loaded(st);
        return;
      }
      self.shelves = st.shelves;
      let at = -1;
      for (let i = 0; i < self.shelves.length; i++) {
        if (self.shelves[i]!.id === self.shelfId) {
          at = i;
        }
      }
      if (at < 0) {
        self.loaded(st);
        return;
      }
      self.shelfList.reset(self.shelves.length, self.pane === 'shelves' ? self.shelfList.focus() : at);
      self.openShelf(self.shelfId, false);
    });
  }

  private shelf(id: string): Shelf | null {
    for (let i = 0; i < this.shelves.length; i++) {
      if (this.shelves[i]!.id === id) {
        return this.shelves[i]!;
      }
    }
    return null;
  }

  /** orderOf returns a shelf's order: Continue Watching and Recently Added keep theirs. */
  private orderOf(id: string): string {
    return id === 'continue' || id === 'recent' ? 'added' : this.order;
  }

  /** loadMore loads the next part of a long shelf as the focus nears the end of what is loaded. */
  private loadMore(): void {
    const self = this;
    if (this.loadingMore || this.next < 0 || this.titleList.focus() < this.titles.length - loadAhead) {
      return;
    }
    this.loadingMore = true;
    const id = this.shelfId;
    const from = this.next;
    this.model.shelf(id, this.orderOf(id), from, function (err, page) {
      self.loadingMore = false;
      if (err || !page || id !== self.shelfId || from !== self.next) {
        return;
      }
      self.titles = self.titles.concat(page.titles);
      self.next = nextFrom(page);
      const at = self.titleList.focus();
      self.titleList.reset(self.titles.length, at);
    });
  }

  private setPane(pane: Pane): void {
    this.pane = pane;
    this.shelfList.setActive(pane === 'shelves');
    this.titleList.setActive(pane === 'titles');
    this.drawFooter();
  }

  private focused(): TitleRow | null {
    return this.titles[this.titleList.focus()] || null;
  }

  private focusChanged(): void {
    const self = this;
    const t = this.focused();
    // The poster of the title before goes at once; the new one loads once
    // the focus rests.
    this.poster.set('', t ? t.name : '');
    this.drawSide();
    clearTimeout(this.posterTimer);
    this.posterTimer = setTimeout(function () {
      const t = self.focused();
      self.poster.set(t && !self.sparse && !hiddenPoster(t) ? t.poster || '' : '', t ? t.name : '');
    }, posterDelayMs);
    this.loadMore();
  }

  private drawSide(): void {
    const t = this.focused();
    if (!t) {
      this.poster.set('', '');
      setText(this.sideName, '');
      setText(this.sideMeta, '');
      setText(this.sideNote, '');
      return;
    }
    setText(this.sideName, t.name);
    setText(this.sideMeta, t.episode ? t.episode : meta(t));
    let note = 'OK for the details.';
    if (hiddenPoster(t)) {
      note = 'Locked. OK, then the PIN, for the details.';
    } else if (t.progress) {
      note = t.progress + '% watched. OK for the details.';
    } else if (t.watched) {
      note = 'Watched. OK for the details.';
    }
    setText(this.sideNote, note);
  }

  key(p: Press): boolean {
    const list = this.pane === 'shelves' ? this.shelfList : this.titleList;
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
        this.setPane('shelves');
        return true;
      case 'right':
        if (this.titles.length) {
          this.openFocusedShelf();
          this.setPane('titles');
        }
        return true;
      case 'back':
        if (this.pane === 'titles') {
          this.setPane('shelves');
          return true;
        }
        return false;
      case 'ok':
        if (this.pane === 'shelves') {
          this.openFocusedShelf();
          if (this.titles.length || this.shelves.length) {
            this.setPane('titles');
          }
        } else {
          this.openTitle();
        }
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
    if (this.pane === 'shelves') {
      const self = this;
      clearTimeout(this.shelfTimer);
      clearTimeout(this.fillTimer);
      if (this.ctx.settings.current.groups !== 'ok') {
        this.shelfTimer = setTimeout(function () {
          const s = self.shelves[self.shelfList.focus()];
          if (s && s.id !== self.shelfId) {
            self.openShelf(s.id, true);
          }
        }, shelfDelayMs);
      }
    } else if (this.sparse) {
      this.fill();
    } else {
      this.focusChanged();
    }
    return true;
  }

  /** openFocusedShelf shows the shelf in focus, in full. */
  private openFocusedShelf(): void {
    const s = this.shelves[this.shelfList.focus()];
    if (!s) {
      return;
    }
    if (s.id !== this.shelfId) {
      this.openShelf(s.id, false);
    } else {
      this.fill();
    }
  }

  private openTitle(): void {
    const self = this;
    const t = this.focused();
    if (!t) {
      return;
    }
    const open = function () {
      self.ctx.go('title', { kind: t.kind || self.kind, id: t.id, name: t.name, play: self.shelfId === 'continue' });
    };
    if (!hiddenPoster(t)) {
      open();
      return;
    }
    this.ctx.unlock(t.name, function (ok) {
      if (ok) {
        // The PIN is kept for a while: the shelf's pictures show again.
        self.titleList.render();
        self.focusChanged();
        open();
      }
    });
  }

  private sort(): void {
    if (this.shelfId === 'continue' || this.shelfId === 'recent') {
      this.ctx.toast(this.shelfName.textContent + ' keeps its own order.');
      return;
    }
    this.order = this.order === 'name' ? 'added' : 'name';
    this.ctx.toast(this.order === 'name' ? 'Sorted by name.' : 'Sorted by when they were added, the latest first.');
    this.openShelf(this.shelfId, false);
    this.drawFooter();
  }

  private drawFooter(): void {
    const sorts = this.shelfId !== 'continue' && this.shelfId !== 'recent' && this.titles.length > 0;
    const left = [{ key: 'blue', text: 'Search' }];
    if (sorts) {
      left.unshift({ key: 'yellow', text: this.order === 'name' ? 'Sort by Date Added' : 'Sort by Name' });
    }
    this.footer.set(
      left,
      this.pane === 'shelves'
        ? [
            { key: 'OK', text: 'Open' },
            { key: 'r', text: this.kind === 'movie' ? 'Movies' : 'Series' },
          ]
        : [
            { key: 'OK', text: 'Details' },
            { key: 'l', text: 'Shelves' },
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

/** countText says how many titles a shelf holds, or nothing while that is not known. */
function countText(n: number): string {
  if (n < 0) {
    return '';
  }
  return n + (n === 1 ? ' title' : ' titles');
}

/** nextFrom returns where the part after page starts, -1 after the last; a server that does not say counts the titles. */
function nextFrom(page: ShelfPage): number {
  if (page.next !== undefined) {
    return page.next;
  }
  const end = page.from + page.titles.length;
  return end < page.total ? end : -1;
}
