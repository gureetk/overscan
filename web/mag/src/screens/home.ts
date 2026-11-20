// SPDX-License-Identifier: AGPL-3.0-or-later

// Home: Simple, six big buttons on a plain background, or With a Picture, a
// picture behind the last channel watched, smaller buttons and the
// favourite channels (D52). The box's settings choose. A button for Radio
// shows when the sources list radio stations (D87), and one for Recordings
// while the server records (D100). When the account has two profiles or
// more, the head names the one watching, and the yellow key switches to
// another (D94).

import type { Channel } from '../api';
import { el, Logo, setClass, setText, setWidth, show } from '../dom';
import { icon } from '../icons';
import type { Press } from '../keys';
import { label } from '../player';
import { pictureURL } from '../settings';
import { clock, now, progress } from '../time';
import { Clock, type Ctx, Footer, type Screen, type VideoArea } from './screen';

interface Tile {
  name: string;
  icon: string;
  line: string;
  open: () => void;
  /** Whether the button shows; always, if missing. */
  when?: () => boolean;
  /** A shorter line for a narrower button, four to a row. */
  short?: string;
}

/** How many favourite channels With a Picture shows. */
const favouriteCards = 5;

export class HomeScreen implements Screen {
  el: HTMLElement;
  tunes = true;
  private ctx: Ctx;
  private tiles: Tile[];
  /** The place of the focused button among those that show. */
  private focusTile = 0;
  /** With a Picture: 0 the buttons, 1 the favourites. */
  private focusRow = 0;
  private focusFav = 0;

  private simple: HTMLElement;
  private bigGrid: HTMLElement;
  private bigTiles: { box: HTMLElement; line: HTMLElement }[] = [];
  private footer: Footer;

  private picture: HTMLElement;
  private pictureImage: HTMLElement;
  private pictureName = '';
  private last: { label: HTMLElement; logo: Logo; channel: HTMLElement; title: HTMLElement; when: HTMLElement; bar: HTMLElement; barBox: HTMLElement; next: HTMLElement };
  private smallRow: HTMLElement;
  private smallTiles: HTMLElement[] = [];
  private favTitle: HTMLElement;
  private favCards: { box: HTMLElement; logo: Logo; bar: HTMLElement }[] = [];
  private clocks: Clock[] = [];
  /** The profile watching, in each layout's head. */
  private chips: { box: HTMLElement; name: HTMLElement }[] = [];

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen home', parent);
    this.tiles = [
      { name: 'Live TV', icon: 'tv', line: '', open: () => ctx.go('live') },
      { name: 'TV Guide', icon: 'guide', line: "What's on now and later", open: () => ctx.go('guide') },
      { name: 'Radio', icon: 'radio', line: '', open: () => ctx.go('radio'), when: () => ctx.lineup.stations.length > 0 },
      { name: 'Movies', icon: 'movies', line: 'Films from your sources', open: () => ctx.go('movies') },
      { name: 'Series', icon: 'series', line: 'Series from your sources', open: () => ctx.go('series') },
      { name: 'Recordings', icon: 'record', line: '', open: () => ctx.go('recordings'), when: () => ctx.recordings.on },
      { name: 'Search', icon: 'search', line: 'Channels, programmes and films', short: 'Channels, guide, films', open: () => ctx.go('search') },
      { name: 'Settings', icon: 'settings', line: 'This box, appearance, about', open: () => ctx.go('settings') },
    ];

    // Simple.
    this.simple = el('div', 'simple', this.el);
    this.header(this.simple);
    this.bigGrid = el('div', 'big-tiles', this.simple);
    for (let i = 0; i < this.tiles.length; i++) {
      const t = this.tiles[i]!;
      const box = el('div', 'big-tile', this.bigGrid);
      el('div', 'tile-icon', box).innerHTML = icon(t.icon, 44, 1.6);
      const text = el('div', 'tile-text', box);
      el('div', 'tile-name', text, t.name);
      const line = el('div', 'tile-line', text, t.line);
      this.bigTiles.push({ box: box, line: line });
    }
    this.footer = new Footer(this.simple);

    // With a Picture.
    this.picture = el('div', 'with-picture', this.el);
    this.pictureImage = el('div', 'picture', this.picture);
    el('div', 'shade-left', this.picture);
    el('div', 'shade-down', this.picture);
    this.header(this.picture);
    const lastBox = el('div', 'last', this.picture);
    const top = el('div', 'last-top', lastBox);
    const lastLabel = el('span', 'last-label', top, 'LAST WATCHED');
    el('span', 'rule', top);
    const lastLogo = new Logo('last-logo', top);
    const lastChannel = el('span', 'last-channel', top);
    const lastTitle = el('div', 'last-title', lastBox);
    const when = el('div', 'last-when', lastBox);
    el('span', 'live-dot', when);
    const whenText = el('span', '', when);
    const nextRow = el('div', 'last-next', lastBox);
    const barBox = el('span', 'bar', nextRow);
    const bar = el('span', 'fill acc', barBox);
    const next = el('span', '', nextRow);
    this.last = { label: lastLabel, logo: lastLogo, channel: lastChannel, title: lastTitle, when: whenText, bar: bar, barBox: barBox, next: next };

    this.smallRow = el('div', 'small-tiles', this.picture);
    for (let i = 0; i < this.tiles.length; i++) {
      const t = this.tiles[i]!;
      const box = el('div', 'small-tile', this.smallRow);
      el('div', 'tile-face', box).innerHTML = icon(t.icon, 28, 1.7);
      el('div', 'tile-name', box, t.name);
      this.smallTiles.push(box);
    }
    this.favTitle = el('div', 'fav-title', this.picture, 'Favourite Channels');
    const favRow = el('div', 'fav-row', this.picture);
    for (let i = 0; i < favouriteCards; i++) {
      const box = el('div', 'fav-card', favRow);
      const logo = new Logo('fav-logo', box);
      const barBox2 = el('span', 'bar', box);
      this.favCards.push({ box: box, logo: logo, bar: el('span', 'fill acc', barBox2) });
    }
  }

  private header(parent: HTMLElement): void {
    const head = el('div', 'head', parent);
    const brand = el('div', 'brand', head);
    el('span', 'mark', brand);
    el('span', 'word', brand, 'Overscan');
    const chip = el('div', 'profile-chip', head);
    el('span', 'swatch yellow', chip);
    const name = el('span', 'name', chip);
    show(chip, false);
    this.chips.push({ box: chip, name: name });
    this.clocks.push(new Clock(head, true));
  }

  /** renderProfile names the profile watching, while the account has two or more. */
  private renderProfile(): void {
    const several = this.ctx.profiles.several();
    const name = several ? this.ctx.profiles.name() : '';
    for (let i = 0; i < this.chips.length; i++) {
      const c = this.chips[i]!;
      show(c.box, !!name);
      setText(c.name, name);
    }
  }

  /** recordingsLine says on the Recordings button how many record now and are to come. */
  private recordingsLine(): void {
    const t = now();
    const going = this.ctx.recordings.underWay(t).length;
    const coming = this.ctx.recordings.toCome(t).length;
    const tile = this.tiles[5]!;
    if (going && coming) {
      tile.line = going + ' recording now, ' + coming + ' to come';
      tile.short = going + ' now, ' + coming + ' to come';
    } else if (going) {
      tile.line = tile.short = going + ' recording now';
    } else if (coming) {
      tile.line = tile.short = coming + ' to come';
    } else {
      tile.line = tile.short = 'Under way and to come';
    }
  }

  private withPicture(): boolean {
    return this.ctx.settings.current.home === 'picture';
  }

  /** shown returns the places in tiles of the buttons that show. */
  private shown(): number[] {
    const out: number[] = [];
    for (let i = 0; i < this.tiles.length; i++) {
      const when = this.tiles[i]!.when;
      if (!when || when()) {
        out.push(i);
      }
    }
    return out;
  }

  /** columns returns how many big buttons a row of Simple holds: four when there are seven or eight. */
  private columns(n: number): number {
    return n > 6 ? 4 : 3;
  }

  enter(): void {
    this.focusRow = 0;
    this.render();
    this.wantGuide();
    const self = this;
    this.ctx.recordings.load(function () {
      self.render();
    });
  }

  leave(): void {}

  private wantGuide(): void {
    const self = this;
    const ids: number[] = [];
    const last = this.ctx.lineup.lastWatched();
    if (last) {
      ids.push(last.id);
    }
    const favs = this.ctx.lineup.favouriteChannels();
    for (let i = 0; i < favs.length && i < favouriteCards; i++) {
      ids.push(favs[i]!.id);
    }
    if (ids.length) {
      this.ctx.guide.wantNow(ids, function () {
        self.render();
      });
    }
  }

  render(): void {
    const picture = this.withPicture();
    show(this.simple, !picture);
    show(this.picture, picture);
    const last = this.ctx.lineup.lastWatched();
    const count = this.ctx.lineup.channels.length;
    this.tiles[0]!.line = last ? 'Last watched: ' + label(last) : count + (count === 1 ? ' channel' : ' channels');
    const stations = this.ctx.lineup.stations.length;
    this.tiles[2]!.line = stations + (stations === 1 ? ' station' : ' stations');
    this.recordingsLine();
    const shown = this.shown();
    this.focusTile = Math.min(this.focusTile, shown.length - 1);
    this.renderProfile();
    if (picture) {
      this.renderPicture(last, shown);
    } else {
      const cols = this.columns(shown.length);
      setClass(this.bigGrid, 'big-tiles' + (cols === 4 ? ' four' : ''));
      for (let i = 0; i < this.bigTiles.length; i++) {
        const t = this.bigTiles[i]!;
        const at = shown.indexOf(i);
        show(t.box, at >= 0);
        setClass(t.box, 'big-tile' + (at === this.focusTile ? ' focus' : '') + ((at + 1) % cols === 0 ? ' row-end' : ''));
        const tile = this.tiles[i]!;
        setText(t.line, cols === 4 && tile.short ? tile.short : tile.line);
      }
      const keys = [
        { key: 'OK', text: 'Open' },
        { key: 'lrud', text: 'Move' },
        { key: '0–9', text: 'Go to channel' },
      ];
      if (this.ctx.profiles.several()) {
        keys.unshift({ key: 'yellow', text: 'Switch Profile' });
      }
      this.footer.set(keys, []);
      this.footer.setRightText(this.ctx.server.label);
    }
  }

  private renderPicture(last: Channel | null, shown: number[]): void {
    const name = this.ctx.settings.current.picture;
    if (name !== this.pictureName) {
      this.pictureName = name;
      this.pictureImage.style.backgroundImage = 'url("' + pictureURL(name) + '")';
    }
    const t = now();
    const l = this.last;
    if (last) {
      setText(l.label, 'LAST WATCHED');
      l.logo.set(last.logo || '', last.name);
      show(l.logo.el, true);
      setText(l.channel, last.number + ' · ' + last.name);
      const p = this.ctx.guide.at(last.id, t);
      const next = this.ctx.guide.after(last.id, p ? p.stop : t);
      setText(l.title, p ? p.title : last.name);
      setText(l.when, p ? 'On now, until ' + clock(p.stop) : 'Press OK on Live TV to watch it again');
      show(l.barBox, !!p);
      if (p) {
        setWidth(l.bar, progress(p.start, p.stop, t));
      }
      setText(l.next, next ? 'Next: ' + next.title : '');
    } else {
      const count = this.ctx.lineup.channels.length;
      setText(l.label, 'WELCOME');
      show(l.logo.el, false);
      setText(l.channel, this.ctx.server.label);
      setText(l.title, count + (count === 1 ? ' channel to watch' : ' channels to watch'));
      setText(l.when, 'Open Live TV to start');
      show(l.barBox, false);
      setText(l.next, '');
    }
    setClass(this.smallRow, 'small-tiles' + (shown.length > 7 ? ' eight' : shown.length > 6 ? ' seven' : ''));
    for (let i = 0; i < this.smallTiles.length; i++) {
      const at = shown.indexOf(i);
      show(this.smallTiles[i]!, at >= 0);
      setClass(this.smallTiles[i]!, 'small-tile' + (this.focusRow === 0 && at === this.focusTile ? ' focus' : ''));
    }
    const favs = this.ctx.lineup.favouriteChannels();
    show(this.favTitle, favs.length > 0);
    if (this.focusFav >= Math.min(favs.length, favouriteCards)) {
      this.focusFav = 0;
    }
    for (let i = 0; i < this.favCards.length; i++) {
      const card = this.favCards[i]!;
      const c = favs[i];
      if (!c) {
        show(card.box, false);
        continue;
      }
      show(card.box, true);
      setClass(card.box, 'fav-card' + (this.focusRow === 1 && i === this.focusFav ? ' focus' : ''));
      card.logo.set(c.logo || '', c.name);
      const p = this.ctx.guide.at(c.id, t);
      setWidth(card.bar, p ? progress(p.start, p.stop, t) : 0);
    }
  }

  key(p: Press): boolean {
    if (p.key === 'yellow' && this.ctx.profiles.several()) {
      this.ctx.switchProfile();
      return true;
    }
    const shown = this.shown();
    const n = shown.length;
    if (!this.withPicture()) {
      const cols = this.columns(n);
      switch (p.key) {
        case 'left':
          this.focusTile = (this.focusTile + n - 1) % n;
          break;
        case 'right':
          this.focusTile = (this.focusTile + 1) % n;
          break;
        case 'up':
        case 'down':
          // Two rows: from the first to the one below, or the last button; from the second, up.
          this.focusTile = this.focusTile < cols ? Math.min(this.focusTile + cols, n - 1) : this.focusTile - cols;
          break;
        case 'ok':
          this.tiles[shown[this.focusTile]!]!.open();
          return true;
        default:
          return false;
      }
      this.render();
      return true;
    }
    const favs = Math.min(this.ctx.lineup.favouriteChannels().length, favouriteCards);
    switch (p.key) {
      case 'left':
        if (this.focusRow === 0) {
          this.focusTile = (this.focusTile + n - 1) % n;
        } else {
          this.focusFav = (this.focusFav + favs - 1) % favs;
        }
        break;
      case 'right':
        if (this.focusRow === 0) {
          this.focusTile = (this.focusTile + 1) % n;
        } else {
          this.focusFav = (this.focusFav + 1) % favs;
        }
        break;
      case 'down':
        if (favs > 0) {
          this.focusRow = 1;
        }
        break;
      case 'up':
        this.focusRow = 0;
        break;
      case 'ok':
        if (this.focusRow === 0) {
          this.tiles[shown[this.focusTile]!]!.open();
        } else {
          const list = this.ctx.lineup.favouriteChannels();
          const c = list[this.focusFav];
          if (c) {
            this.ctx.watch(c, list, true, 'fav');
          }
        }
        return true;
      case 'red':
        if (this.focusRow === 1) {
          const c = this.ctx.lineup.favouriteChannels()[this.focusFav];
          if (c) {
            this.ctx.toggleFavourite(c);
          }
          return true;
        }
        return false;
      default:
        return false;
    }
    this.render();
    return true;
  }

  tick(): void {
    for (let i = 0; i < this.clocks.length; i++) {
      this.clocks[i]!.tick();
    }
    if (this.withPicture()) {
      this.wantGuide();
      this.render();
    }
  }

  lineupChanged(): void {
    this.render();
    this.wantGuide();
  }

  playback(): void {}

  video(): VideoArea {
    return 'none';
  }

  /** favouritesChanged redraws the favourites, after one is added or removed. */
  favouritesChanged(): void {
    this.render();
  }

  /** profilesChanged redraws the profile watching, after the box switched or the profiles changed. */
  profilesChanged(): void {
    this.render();
  }
}
