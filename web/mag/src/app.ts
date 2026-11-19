// SPDX-License-Identifier: AGPL-3.0-or-later

// The page: pairing the box, loading the lineup and the box's settings,
// the screens and moving between them, the remote's keys, and where the
// video shows (D52).

import { type ApiError, type Channel, Client, describe, type Lineup, type Programme, type Session } from './api';
import { PlaybackCheck } from './check';
import { el, show } from './dom';
import { FilmPlayer, pastProgramme, type Playing, playingName } from './film';
import { GuideCache } from './guide';
import { keyOf, type Press } from './keys';
import { LibraryModel } from './library';
import { LineupModel } from './lineup';
import { type Modal, NumberBox, type PickItem, Picker, PinDialog, Toast } from './overlays';
import type { Platform, Rect } from './platform';
import { label, Player } from './player';
import { CheckScreen } from './screens/check';
import { EpisodesScreen } from './screens/episodes';
import { FilmScreen } from './screens/film';
import { GuideScreen } from './screens/guide';
import { HomeScreen } from './screens/home';
import { LibraryScreen } from './screens/library';
import { LiveScreen } from './screens/live';
import { type MessageArg, MessageScreen } from './screens/message';
import type { Ctx, Screen, ScreenName } from './screens/screen';
import { SearchScreen } from './screens/search';
import { SettingsScreen } from './screens/settings';
import { findEpisode, TitleScreen } from './screens/title';
import { WatchScreen } from './screens/watch';
import { keepPIN, keptPIN } from './pin';
import { readSecret, writeSecret } from './secret';
import { SettingsModel } from './settings';

/** How long the page waits for another digit of a channel number. */
const digitWaitMs = 1500;
const volumeStep = 5;
/** How often the page asks whether the lineup changed (D52). */
const versionPollMs = 5 * 60 * 1000;
const tickMs = 10000;

export class App {
  private platform: Platform;
  private client: Client;
  private lineup: LineupModel;
  private guide: GuideCache;
  private settings: SettingsModel;
  private player: Player;
  private film: FilmPlayer;
  private check: PlaybackCheck;
  private ctx: Ctx;
  private server = { version: '', label: '' };

  private screens: { [name: string]: Screen } = {};
  private stack: ScreenName[] = [];
  private current: Screen;
  private message: MessageScreen;

  private toastView: Toast;
  private numberBox: NumberBox;
  private pin: PinDialog;
  private picker: Picker;
  private hole: VideoHole;
  private window = '';

  private timer = 0;
  private versionTimer = 0;
  private digits = '';
  private digitTimer = 0;
  /** A channel asked for full screen, until it plays. */
  private wantFull = false;
  private loaded = false;

  constructor(root: HTMLElement, platform: Platform) {
    const self = this;
    this.platform = platform;
    this.client = new Client(platform.mac, platform.model, readSecret(platform.secretFile), function (secret) {
      writeSecret(platform.secretFile, secret);
    });
    this.lineup = new LineupModel();
    this.guide = new GuideCache(this.client);
    this.settings = new SettingsModel(this.client);
    this.lineup.numbering = function () {
      return self.settings.current.numbers;
    };
    this.player = new Player(platform, this.client, {
      askPIN: function (c, message, done) {
        self.pin.ask(label(c), message, function (pin) {
          if (!pin) {
            self.wantFull = false;
          }
          done(pin);
        });
      },
      playing: function (c) {
        self.lineup.played(c.id);
        if (self.wantFull && self.stack[self.stack.length - 1] !== 'watch') {
          self.wantFull = false;
          self.go('watch');
        }
        self.wantFull = false;
        self.current.playback();
        self.applyVideo();
      },
      stopped: function (text) {
        self.wantFull = false;
        if (text) {
          self.toast(text);
        }
        if (self.stack[self.stack.length - 1] === 'watch') {
          self.back();
        } else {
          self.current.playback();
          self.applyVideo();
        }
      },
      lost: function (err) {
        self.lost(err);
      },
    });

    const movies = new LibraryModel(this.client, 'movie');
    const series = new LibraryModel(this.client, 'series');
    this.film = new FilmPlayer(platform, this.client, {
      askPIN: function (what, message, done) {
        self.pin.ask(what, message, done);
      },
      notice: function (text) {
        self.toast(text);
      },
      stopped: function (text) {
        if (text) {
          self.toast(text);
        }
        if (self.stack[self.stack.length - 1] === 'film') {
          self.back();
        } else {
          self.applyVideo();
        }
      },
      ended: function (p) {
        self.filmEnded(p);
      },
      lost: function (err) {
        if (err.code === 'not_found') {
          self.toast('That is no longer in your source.');
          if (self.stack[self.stack.length - 1] === 'film') {
            self.back();
          }
          return;
        }
        self.lost(err);
      },
      changed: function () {
        if (self.current === self.screens.film) {
          (self.current as FilmScreen).changed();
        }
      },
    });

    this.check = new PlaybackCheck(platform, this.client, {
      changed: function () {
        if (self.current === self.screens.check) {
          (self.current as CheckScreen).changed();
        } else if (self.current === self.screens.settings) {
          (self.current as SettingsScreen).checkChanged();
        }
      },
      lost: function (err) {
        self.lost(err);
      },
    });

    this.ctx = {
      platform: platform,
      client: this.client,
      lineup: this.lineup,
      guide: this.guide,
      settings: this.settings,
      player: this.player,
      movies: movies,
      series: series,
      film: this.film,
      check: this.check,
      server: this.server,
      go: function (name, arg) {
        self.go(name, arg);
      },
      back: function () {
        self.back();
      },
      toast: function (text) {
        self.toast(text);
      },
      watch: function (c, list, full, key) {
        self.watch(c, list, full, key);
      },
      toggleFavourite: function (c) {
        self.toggleFavourite(c);
      },
      playFilm: function (p, from) {
        self.playFilm(p, from);
      },
      playProgramme: function (c: Channel, p: Programme) {
        self.playFilm(pastProgramme(c, p), 0);
      },
      pick: function (title: string, items: PickItem[], chosen: number, done: (index: number) => void) {
        self.picker.pick(title, items, chosen, done);
      },
      unlock: function (what: string, done: (ok: boolean) => void) {
        self.unlock(what, '', done);
      },
      videoChanged: function () {
        self.applyVideo();
      },
    };

    this.hole = new VideoHole(root);
    const screensBox = el('div', 'screens', root);
    this.message = new MessageScreen(screensBox);
    this.screens = {
      message: this.message,
      home: new HomeScreen(screensBox, this.ctx),
      live: new LiveScreen(screensBox, this.ctx),
      watch: new WatchScreen(screensBox, this.ctx),
      guide: new GuideScreen(screensBox, this.ctx),
      search: new SearchScreen(screensBox, this.ctx),
      settings: new SettingsScreen(screensBox, this.ctx),
      movies: new LibraryScreen(screensBox, this.ctx, movies),
      series: new LibraryScreen(screensBox, this.ctx, series),
      title: new TitleScreen(screensBox, this.ctx),
      episodes: new EpisodesScreen(screensBox, this.ctx),
      film: new FilmScreen(screensBox, this.ctx),
      check: new CheckScreen(screensBox, this.ctx),
    };
    for (const name in this.screens) {
      show(this.screens[name]!.el, false);
    }
    this.current = this.message;

    // Outside the page's main element, so they can show over the video.
    const changed = function () {
      self.applyVideo();
    };
    this.toastView = new Toast(document.body, changed);
    this.numberBox = new NumberBox(document.body);
    this.pin = new PinDialog(document.body, changed);
    this.picker = new Picker(document.body, changed);
  }

  start(): void {
    const self = this;
    this.platform.init(function (event) {
      if (self.check.active()) {
        self.check.onEvent(event);
      } else if (self.film.item) {
        self.film.onEvent(event);
      } else {
        self.player.onEvent(event);
      }
    });
    document.addEventListener(
      'keydown',
      function (e) {
        self.onKey(e);
      },
      false,
    );
    window.addEventListener(
      'unload',
      function () {
        self.platform.stop();
      },
      false,
    );
    setInterval(function () {
      self.current.tick();
    }, tickMs);
    this.showMessage({ title: 'Connecting…', text: 'Overscan is starting.' });
    this.connect();
  }

  // Connecting and loading.

  private connect(): void {
    const self = this;
    clearTimeout(this.timer);
    this.client.start(function (err, s) {
      if (err && err.status === 400) {
        self.showMessage({ title: 'This box was not recognised', text: 'The server refused its MAC address, ' + self.platform.mac + '.' });
        return;
      }
      if (err || !s) {
        self.showMessage({ title: "Can't reach Overscan", text: 'Trying again in a few seconds.', detail: location.host });
        self.later(5000, self.connect);
        return;
      }
      self.connected(s);
    });
  }

  private connected(s: Session): void {
    this.server.version = s.version;
    if (s.state === 'approved') {
      this.server.label = s.label || '';
      this.load();
    } else if (s.state === 'pending') {
      this.showMessage({
        title: 'Waiting for approval',
        text: 'To let this box in, approve it in the admin, or run this on the server. The channels appear by themselves once it is approved.',
        detail: 'overscan devices approve ' + (s.code || s.mac),
      });
      this.later(5000, this.connect);
    } else {
      this.showMessage({ title: 'This box is blocked', text: 'To let it in again, approve it in the admin, or run this on the server:', detail: 'overscan devices approve ' + s.mac });
      this.later(30000, this.connect);
    }
  }

  /** load loads the box's settings and the lineup, then shows Home. */
  private load(): void {
    const self = this;
    clearTimeout(this.timer);
    this.settings.load(function () {
      self.loadLineup(function () {
        if (!self.loaded) {
          self.loaded = true;
          self.home();
          self.pollVersion();
        }
      });
    });
  }

  private loadLineup(done: () => void): void {
    const self = this;
    this.client.call<Lineup>('GET', 'channels', null, function (err, lineup) {
      if (err && (err.code === 'unauthorized' || err.code === 'blocked')) {
        self.loaded = false;
        self.connect();
        return;
      }
      if (err || !lineup) {
        if (!self.loaded) {
          self.showMessage({ title: "Can't load the channels", text: describe(err) });
          self.later(10000, self.load);
        }
        return;
      }
      if (lineup.channels.length === 0) {
        self.player.stop();
        self.loaded = false;
        self.showMessage({
          title: 'No channels yet',
          text: "Add a source in the admin, under Sources. The channels appear here once it has been read.",
        });
        self.later(30000, self.load);
        return;
      }
      self.setLineup(lineup);
      done();
    });
  }

  private setLineup(lineup: Lineup): void {
    const playing = this.player.channel;
    this.lineup.set(lineup);
    if (playing) {
      const c = this.lineup.channel(playing.id);
      if (c) {
        this.player.channel = c;
      } else {
        this.player.stop();
      }
    }
    if (this.loaded) {
      for (const name in this.screens) {
        if (name !== 'message') {
          this.screens[name]!.lineupChanged();
        }
      }
      this.applyVideo();
    }
  }

  /** pollVersion asks now and then whether the lineup changed, and loads it again if it did. */
  private pollVersion(): void {
    const self = this;
    clearTimeout(this.versionTimer);
    this.versionTimer = setTimeout(function () {
      self.client.call<{ version: string }>('GET', 'channels/version', null, function (err, v) {
        if (err && (err.code === 'unauthorized' || err.code === 'blocked')) {
          self.loaded = false;
          self.connect();
          return;
        }
        if (!err && v && v.version !== self.lineup.version) {
          self.loadLineup(function () {});
        }
        self.pollVersion();
      });
    }, versionPollMs);
  }

  /** later calls fn in ms, replacing the call that was waiting. */
  private later(ms: number, fn: () => void): void {
    const self = this;
    clearTimeout(this.timer);
    this.timer = setTimeout(function () {
      fn.call(self);
    }, ms);
  }

  private lost(err: ApiError): void {
    if (err.code === 'not_found') {
      this.toast('That channel is no longer in the lineup.');
      this.loadLineup(function () {});
    } else {
      this.loaded = false;
      this.connect();
    }
  }

  // Screens.

  private showMessage(arg: MessageArg): void {
    this.player.stop();
    this.film.stop(true);
    this.stack = [];
    this.switchTo(this.message, arg);
  }

  private switchTo(screen: Screen, arg: any): void {
    if (this.current !== screen) {
      this.current.leave();
      show(this.current.el, false);
      this.current = screen;
    }
    show(screen.el, true);
    screen.enter(arg);
    this.applyVideo();
  }

  private home(): void {
    this.stack = ['home'];
    this.switchTo(this.screens.home!, undefined);
  }

  /** go shows a screen; a screen already on the way back is returned to rather than opened again. */
  private go(name: ScreenName, arg?: any): void {
    if (!this.loaded) {
      return;
    }
    const at = this.stack.indexOf(name);
    if (at >= 0) {
      this.stack.length = at + 1;
    } else {
      this.stack.push(name);
    }
    this.switchTo(this.screens[name]!, arg);
  }

  private back(): void {
    if (this.stack.length <= 1) {
      this.home();
      return;
    }
    const leaving = this.stack.pop();
    const name = this.stack[this.stack.length - 1]!;
    this.switchTo(this.screens[name]!, leaving === 'watch' ? { channel: this.player.channel } : undefined);
  }

  /** unlock asks for the PIN for what, saying why, unless the page keeps it, and checks it with the server. */
  private unlock(what: string, message: string, done: (ok: boolean) => void): void {
    const self = this;
    if (keptPIN()) {
      done(true);
      return;
    }
    this.pin.ask(what, message, function (pin) {
      if (!pin) {
        done(false);
        return;
      }
      self.client.call<{ ok: boolean }>('POST', 'pin', { pin: pin }, function (err) {
        if (!err) {
          keepPIN(pin);
          done(true);
          return;
        }
        if (err.code === 'pin_wrong' || err.code === 'pin_required') {
          self.unlock(what, 'That PIN is not right. Try again.', done);
          return;
        }
        if (err.code === 'pin_locked') {
          const wait = err.retryAfter ? Math.ceil(err.retryAfter / 60) : 5;
          self.toast('Too many wrong PINs. Try again in ' + wait + (wait === 1 ? ' minute.' : ' minutes.'));
        } else if (err.code === 'unauthorized' || err.code === 'blocked') {
          self.lost(err);
        } else {
          self.toast("Can't check the PIN. " + describe(err));
        }
        done(false);
      });
    });
  }

  private toast(text: string): void {
    this.toastView.show(text);
  }

  private watch(c: Channel, list: Channel[], full: boolean, key: string): void {
    const playing = this.player.channel;
    if (playing && playing.id === c.id) {
      if (full) {
        this.go('watch');
      }
      return;
    }
    this.wantFull = full;
    this.player.play(c, list, key);
  }

  /** playFilm plays a film or an episode full screen, in place of a channel. */
  private playFilm(p: Playing, from: number): void {
    if (this.player.channel) {
      this.player.stop();
      this.current.playback();
    }
    this.film.play(p, from);
    this.go('film');
  }

  /** filmEnded goes on to a series' next episode, or back to what opened the film or the programme. */
  private filmEnded(p: Playing): void {
    const next = p.episode ? this.nextEpisode(p) : null;
    if (next) {
      this.toast('Next: ' + playingName(next));
      this.film.play(next, 0);
      return;
    }
    if (this.stack[this.stack.length - 1] === 'film') {
      this.back();
    }
  }

  /** nextEpisode returns the episode after the one that played, if there is one. */
  private nextEpisode(p: Playing): Playing | null {
    const seasons = p.title.seasons || [];
    const at = p.episode ? findEpisode(p.title, p.episode.id) : null;
    if (!at) {
      return null;
    }
    let after = false;
    for (let i = 0; i < seasons.length; i++) {
      const eps = seasons[i]!.episodes;
      for (let j = 0; j < eps.length; j++) {
        if (after) {
          return { title: p.title, episode: eps[j]!, season: seasons[i]!.number };
        }
        after = eps[j]!.id === at.episode.id;
      }
    }
    return null;
  }

  private toggleFavourite(c: Channel): void {
    const self = this;
    const on = !this.lineup.isFavourite(c.id);
    this.lineup.setFavourite(c.id, on);
    this.favouritesChanged();
    this.toast(on ? 'Added ' + label(c) + ' to Favourites.' : 'Removed ' + label(c) + ' from Favourites.');
    this.client.call<{ favourite: boolean }>(on ? 'PUT' : 'DELETE', 'channels/' + c.id + '/favourite', null, function (err) {
      if (err) {
        self.lineup.setFavourite(c.id, !on);
        self.favouritesChanged();
        self.toast("Couldn't change the favourites. " + describe(err));
      }
    });
  }

  private favouritesChanged(): void {
    for (const name in this.screens) {
      const s = this.screens[name] as any;
      if (s.favouritesChanged) {
        s.favouritesChanged();
      }
    }
  }

  /**
   * applyVideo puts the video where the screen wants it: in a window with
   * the page drawn around it, full screen under the page while anything
   * shows over it, or full screen over the page. A screen without video
   * stops it.
   */
  private applyVideo(): void {
    const area = this.current.video();
    const root = document.documentElement;
    if (area === 'none' || (!this.player.channel && !this.film.item && !this.check.playing())) {
      if (area === 'none' && this.player.channel) {
        this.player.stop();
        this.current.playback();
      }
      this.hole.hide();
      this.setWindow(null);
      root.className = root.className.replace(/\s*\bvideo\b/g, '');
      this.platform.videoOnTop(false);
      return;
    }
    if (!/\bvideo\b/.test(root.className)) {
      root.className += ' video';
    }
    if (area === 'full') {
      this.hole.hide();
      this.setWindow(null);
      const over =
        (this.current.overVideo && this.current.overVideo()) ||
        this.toastView.visible() ||
        this.numberBox.visible() ||
        this.pin.visible() ||
        this.picker.visible();
      this.platform.videoOnTop(!over);
    } else {
      this.setWindow(area);
      this.hole.show(area);
      this.platform.videoOnTop(false);
    }
  }

  private setWindow(rect: Rect | null): void {
    const key = rect ? [rect.x, rect.y, rect.w, rect.h].join(',') : '';
    if (key !== this.window) {
      this.window = key;
      this.platform.setWindow(rect);
    }
  }

  // Keys.

  private modal(): Modal | null {
    if (this.pin.visible()) {
      return this.pin;
    }
    if (this.picker.visible()) {
      return this.picker;
    }
    return null;
  }

  private onKey(e: KeyboardEvent): void {
    const p = keyOf(e);
    if (!p) {
      return;
    }
    e.preventDefault(); // Back is Backspace and Channel next is Tab, which the browser would act on
    switch (p.key) {
      case 'volumeUp':
        this.toast('Volume ' + this.platform.changeVolume(volumeStep));
        return;
      case 'volumeDown':
        this.toast('Volume ' + this.platform.changeVolume(-volumeStep));
        return;
      case 'mute':
        this.toast(this.platform.toggleMute() ? 'Sound off' : 'Sound on');
        return;
    }
    const modal = this.modal();
    if (modal) {
      modal.key(p);
      return;
    }
    if (this.digits !== '') {
      if (p.key === 'digit') {
        this.digit(p.digit);
        return;
      }
      if (p.key === 'ok') {
        this.tuneDigits();
        return;
      }
      this.clearDigits();
      if (p.key === 'back') {
        return;
      }
    }
    if (this.current === this.message) {
      return;
    }
    if (this.current.key(p)) {
      return;
    }
    this.globalKey(p);
  }

  private globalKey(p: Press): void {
    switch (p.key) {
      case 'digit':
        this.digit(p.digit);
        break;
      case 'menu':
        this.home();
        break;
      case 'exit':
        if (this.stack[this.stack.length - 1] !== 'home') {
          this.home();
        }
        break;
      case 'back':
        this.back();
        break;
      case 'tv':
        this.go(this.player.channel ? 'watch' : 'live');
        break;
      case 'guide':
        this.go('guide');
        break;
      case 'stop':
        if (this.player.channel) {
          this.player.stop();
          this.current.playback();
          this.applyVideo();
        }
        break;
    }
  }

  /** numberingKey returns the group whose numbers a typed number is, as the screen sees it (D56). */
  private numberingKey(): string {
    return this.current.numbering ? this.current.numbering() : 'all';
  }

  /**
   * digit adds a digit to the channel number being typed. Once it has as
   * many digits as the highest channel number, no other channel can match,
   * so it tunes at once; otherwise it waits a moment for another digit.
   */
  private digit(d: number): void {
    const self = this;
    if (!this.current.tunes) {
      return;
    }
    this.digits += String(d);
    const key = this.numberingKey();
    const c = this.lineup.find(key, parseInt(this.digits, 10));
    this.numberBox.show(this.digits, c ? c.name : '');
    this.applyVideo();
    clearTimeout(this.digitTimer);
    if (this.digits.length >= this.lineup.digitsIn(key)) {
      this.tuneDigits();
      return;
    }
    this.digitTimer = setTimeout(function () {
      self.tuneDigits();
    }, digitWaitMs);
  }

  private clearDigits(): void {
    clearTimeout(this.digitTimer);
    this.digits = '';
    this.numberBox.hide();
    this.applyVideo();
  }

  /** tuneDigits shows the channel whose number has been typed. */
  private tuneDigits(): void {
    const n = parseInt(this.digits, 10);
    this.clearDigits();
    const key = this.numberingKey();
    const c = this.lineup.find(key, n);
    if (!c) {
      const g = this.lineup.fromOne(key) ? this.lineup.group(key) : null;
      this.toast('There is no channel ' + n + (g ? ' in ' + g.name : '') + '.');
      return;
    }
    if (this.current.tune) {
      this.current.tune(c);
    } else {
      this.watch(c, this.lineup.channels, true, 'all');
    }
  }
}

/**
 * VideoHole draws the page's background around a window that the video
 * shows through, as four panels and rounded corners, while the page itself
 * is transparent.
 */
class VideoHole {
  private panels: HTMLElement[] = [];
  private corners: HTMLElement;

  constructor(root: HTMLElement) {
    for (let i = 0; i < 4; i++) {
      const p = el('div', 'hole-panel', root);
      show(p, false);
      this.panels.push(p);
    }
    this.corners = el('div', 'hole-corners', root);
    show(this.corners, false);
  }

  show(r: Rect): void {
    const w = window.innerWidth;
    const h = window.innerHeight;
    const boxes = [
      [0, 0, w, r.y],
      [0, r.y + r.h, w, h - r.y - r.h],
      [0, r.y, r.x, r.h],
      [r.x + r.w, r.y, w - r.x - r.w, r.h],
    ];
    for (let i = 0; i < 4; i++) {
      const b = boxes[i]!;
      const s = this.panels[i]!.style;
      s.left = b[0] + 'px';
      s.top = b[1] + 'px';
      s.width = Math.max(0, b[2]!) + 'px';
      s.height = Math.max(0, b[3]!) + 'px';
      show(this.panels[i]!, true);
    }
    const s = this.corners.style;
    s.left = r.x + 'px';
    s.top = r.y + 'px';
    s.width = r.w + 'px';
    s.height = r.h + 'px';
    show(this.corners, true);
  }

  hide(): void {
    for (let i = 0; i < 4; i++) {
      show(this.panels[i]!, false);
    }
    show(this.corners, false);
  }
}

