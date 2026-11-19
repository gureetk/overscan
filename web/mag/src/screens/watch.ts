// SPDX-License-Identifier: AGPL-3.0-or-later

// Watching full screen, with the info banner: the channel, what is on now
// and next, what the picture and sound are, and what the colour keys do.
// While the server keeps the channel in a timeshift buffer (D92), Play and
// Pause or Green pause it, Left, Right, Rewind and Fast-forward go back and
// on in it, and Stop goes back to live.

import type { Channel, StreamAudio } from '../api';
import { el, Logo, setHTML, setText, setWidth, show } from '../dom';
import { star } from '../icons';
import type { Press } from '../keys';
import type { PickItem } from '../overlays';
import { clock, duration, hms, now, progress, span } from '../time';
import { Clock, type Ctx, escape, Footer, type Screen, type VideoArea } from './screen';

/** How long the banner shows by itself. */
const bannerMs = 6000;

export class WatchScreen implements Screen {
  el: HTMLElement;
  tunes = true;
  private ctx: Ctx;
  private banner: HTMLElement;
  private logo: Logo;
  private number: HTMLElement;
  private name: HTMLElement;
  private fav: HTMLElement;
  private nowLine: HTMLElement;
  private liveDot: HTMLElement;
  private nowWord: HTMLElement;
  private nowText: HTMLElement;
  private badges: HTMLElement;
  private title: HTMLElement;
  private barRow: HTMLElement;
  private bar: HTMLElement;
  private left: HTMLElement;
  private next: HTMLElement;
  private footer: Footer;
  private clockView: Clock;
  private timer = 0;
  private infoTimer = 0;
  /** Redraws the banner each second while the channel is paused or behind live. */
  private shiftTimer = 0;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen watch', parent);
    this.banner = el('div', 'banner', this.el);
    const top = el('div', 'banner-top', this.banner);
    const who = el('div', 'who', top);
    this.logo = new Logo('banner-logo', who);
    const id = el('div', 'id', who);
    this.number = el('span', 'number', id);
    this.name = el('span', 'name', id);
    this.fav = el('span', 'fav', id);
    this.fav.innerHTML = star(15);
    const what = el('div', 'what', top);
    this.nowLine = el('div', 'now-line', what);
    this.liveDot = el('span', 'live-dot', this.nowLine);
    this.nowWord = el('span', 'now-word', this.nowLine, 'NOW');
    this.nowText = el('span', 'now-text', this.nowLine);
    this.badges = el('span', 'badges', this.nowLine);
    this.title = el('div', 'title', what);
    this.barRow = el('div', 'bar-row', what);
    const barBox = el('div', 'bar', this.barRow);
    this.bar = el('div', 'fill acc', barBox);
    this.left = el('span', 'left', this.barRow);
    this.next = el('div', 'next', what);
    const right = el('div', 'banner-clock', top);
    this.clockView = new Clock(right, false);
    this.footer = new Footer(this.banner);
    show(this.banner, false);
  }

  enter(): void {
    this.showBanner();
  }

  leave(): void {
    this.hideBanner();
  }

  overVideo(): boolean {
    return this.banner.style.display !== 'none';
  }

  private showBanner(): void {
    const self = this;
    this.draw();
    show(this.banner, true);
    this.ctx.videoChanged();
    clearTimeout(this.timer);
    this.timer = setTimeout(function () {
      // A paused channel keeps its banner.
      if (self.ctx.player.paused()) {
        self.showBanner();
      } else {
        self.hideBanner();
      }
    }, bannerMs);
    // What the picture and sound are is known a moment after a stream starts.
    clearTimeout(this.infoTimer);
    this.infoTimer = setTimeout(function () {
      self.drawBadges();
    }, 1500);
    clearInterval(this.shiftTimer);
    if (this.ctx.player.shifted()) {
      this.shiftTimer = setInterval(function () {
        if (self.overVideo()) {
          self.draw();
        }
      }, 1000);
    }
  }

  private hideBanner(): void {
    clearTimeout(this.timer);
    clearTimeout(this.infoTimer);
    clearInterval(this.shiftTimer);
    if (this.banner.style.display !== 'none') {
      show(this.banner, false);
      this.ctx.videoChanged();
    }
  }

  private draw(): void {
    const c = this.ctx.player.channel;
    if (!c) {
      return;
    }
    // Behind live, the banner says what shows, and how far behind it is.
    const player = this.ctx.player;
    const shifted = player.shifted();
    const behind = shifted ? player.behind() : 0;
    const t = now() - behind;
    show(this.liveDot, !shifted);
    setText(this.nowWord, !shifted ? 'NOW' : (player.paused() ? 'PAUSED ' : '') + (behind >= 1 ? '−' + hms(behind) : 'LIVE'));
    this.clockView.tick();
    this.logo.set(c.logo || '', c.name);
    setText(this.number, String(this.ctx.lineup.shownNumber(c, player.listKey, player.index)));
    setText(this.name, c.name);
    const favourite = this.ctx.lineup.isFavourite(c.id);
    show(this.fav, favourite);
    const p = this.ctx.guide.at(c.id, t);
    const next = this.ctx.guide.after(c.id, p ? p.stop : t);
    setText(this.nowText, p ? span(p.start, p.stop) : '');
    setText(this.title, p ? p.title : c.name);
    show(this.barRow, !!p);
    if (p) {
      setWidth(this.bar, progress(p.start, p.stop, t));
      setText(this.left, duration(p.stop - t) + ' left');
    }
    setHTML(this.next, next ? '<span class="at">Next ' + clock(next.start) + '</span>' + escape(next.title) : '');
    this.drawBadges();
    const colours = [{ key: 'red', text: favourite ? 'Remove from Favourites' : 'Add to Favourites' }];
    if (player.shiftable()) {
      colours.push({ key: 'green', text: player.paused() ? 'Play' : 'Pause' });
    }
    colours.push({ key: 'yellow', text: 'Audio' }, { key: 'blue', text: 'Guide' });
    this.footer.set(
      colours,
      shifted
        ? [
            { key: 'lr', text: 'Back and on' },
            { key: 'STOP', text: 'Live' },
          ]
        : [
            { key: 'CH +/−', text: 'Change channel' },
            { key: 'OK', text: 'Channel list' },
          ],
    );
  }

  private drawBadges(): void {
    const v = this.ctx.platform.videoInfo();
    let html = '';
    const h = v.height;
    if (h > 0) {
      html += badge(h >= 2000 ? '4K' : h >= 1000 ? 'FHD' : h >= 700 ? 'HD' : 'SD');
    }
    if (v.fps > 0) {
      html += badge(Math.round(v.fps) + ' FPS');
    }
    const tracks = this.ctx.platform.audioTracks();
    if (tracks.length > 1) {
      const pid = this.ctx.platform.audioTrack();
      for (let i = 0; i < tracks.length; i++) {
        if (tracks[i]!.pid === pid && tracks[i]!.lang) {
          html += badge(tracks[i]!.lang.toUpperCase());
        }
      }
    }
    setHTML(this.badges, html);
  }

  key(p: Press): boolean {
    const player = this.ctx.player;
    if (player.shiftable() && this.shiftKey(p)) {
      this.showBanner();
      return true;
    }
    switch (p.key) {
      case 'up':
      case 'pageUp':
      case 'channelNext':
        this.ctx.player.zap(1);
        return true;
      case 'down':
      case 'pageDown':
      case 'channelPrev':
        this.ctx.player.zap(-1);
        return true;
      case 'ok':
        this.ctx.go('live', { channel: this.ctx.player.channel });
        return true;
      case 'info':
      case 'left':
      case 'right':
        if (this.overVideo() && p.key === 'info') {
          this.hideBanner();
        } else {
          this.showBanner();
        }
        return true;
      case 'red': {
        const c = this.ctx.player.channel;
        if (c) {
          this.ctx.toggleFavourite(c);
          this.showBanner();
        }
        return true;
      }
      case 'yellow':
      case 'audio':
        this.chooseAudio();
        return true;
      case 'blue':
      case 'guide':
        this.ctx.go('guide');
        return true;
      case 'stop':
        this.ctx.player.stop();
        this.ctx.back();
        return true;
    }
    return false;
  }

  /**
   * shiftKey handles a key that pauses the channel or moves through its
   * timeshift buffer, and reports whether it did: Left and Right only once
   * it is paused or behind live, and Stop only then too.
   */
  private shiftKey(p: Press): boolean {
    const player = this.ctx.player;
    const shifted = player.shifted();
    switch (p.key) {
      case 'playPause':
      case 'green':
        player.pauseToggle();
        return true;
      case 'rewind':
        player.seekShift(-60);
        return true;
      case 'forward':
        if (shifted) {
          player.seekShift(60);
        }
        return true;
      case 'left':
        if (shifted) {
          player.seekShift(-10);
          return true;
        }
        return false;
      case 'right':
        if (shifted) {
          player.seekShift(30);
          return true;
        }
        return false;
      case 'stop':
        if (shifted) {
          player.live();
          return true;
        }
        return false;
    }
    return false;
  }

  private chooseAudio(): void {
    const self = this;
    this.hideBanner();
    chooseAudio(this.ctx, 'This channel', function () {
      self.showBanner();
    });
  }

  tick(): void {
    this.clockView.tick();
    if (this.overVideo()) {
      this.draw();
    }
  }

  lineupChanged(): void {}

  favouritesChanged(): void {
    if (this.overVideo()) {
      this.draw();
    }
  }

  playback(): void {
    const c = this.ctx.player.channel;
    if (!c) {
      return;
    }
    const self = this;
    this.showBanner();
    this.ctx.guide.wantNow([c.id], function () {
      if (self.overVideo()) {
        self.draw();
      }
    });
  }

  numbering(): string {
    return this.ctx.player.listKey || 'all';
  }

  tune(c: Channel): void {
    const player = this.ctx.player;
    if (player.list.indexOf(c) >= 0) {
      this.ctx.watch(c, player.list, true, player.listKey);
    } else {
      this.ctx.watch(c, this.ctx.lineup.channels, true, 'all');
    }
  }

  video(): VideoArea {
    return 'full';
  }
}

/**
 * chooseAudio lets the viewer pick a sound track of what plays, if it has
 * more than one; what names it in a message, such as This channel. named,
 * when the server says what the tracks are, in the box's order, names them
 * better than the box can. done is called once a track is picked.
 */
export function chooseAudio(ctx: Ctx, what: string, done: () => void, named?: StreamAudio[] | null): void {
  const tracks = ctx.platform.audioTracks();
  if (tracks.length < 2) {
    ctx.toast(tracks.length ? what + ' has one sound track.' : 'The sound tracks are not known yet.');
    return;
  }
  const pid = ctx.platform.audioTrack();
  const known = named && named.length === tracks.length ? named : null;
  let chosen = 0;
  const items = [];
  for (let i = 0; i < tracks.length; i++) {
    const t = tracks[i]!;
    if (t.pid === pid) {
      chosen = i;
    }
    const n = known ? known[i]! : null;
    if (n) {
      const lang = language(n.language || t.lang);
      items.push({ label: lang || n.title || 'Track ' + (i + 1), detail: soundDetail(n, lang !== '') });
    } else {
      items.push({ label: language(t.lang) || 'Track ' + (i + 1), detail: t.lang ? t.lang.toUpperCase() : '' });
    }
  }
  ctx.pick('Sound', items, chosen, function (i) {
    const t = tracks[i];
    if (t) {
      ctx.platform.setAudioTrack(t.pid);
      done();
    }
  });
}

/** trackItems names a stream's sound tracks as the server lists them, for the Sound menu. */
export function trackItems(named: StreamAudio[]): PickItem[] {
  const items: PickItem[] = [];
  for (let i = 0; i < named.length; i++) {
    const n = named[i]!;
    const lang = language(n.language || '');
    items.push({ label: lang || n.title || 'Track ' + (i + 1), detail: soundDetail(n, lang !== '') });
  }
  return items;
}

/** Names of sound codecs, as ffmpeg names them. */
const codecs: { [codec: string]: string } = {
  aac: 'AAC',
  ac3: 'Dolby Digital',
  eac3: 'Dolby Digital Plus',
  truehd: 'Dolby TrueHD',
  dts: 'DTS',
  flac: 'FLAC',
  mp2: 'MPEG audio',
  mp3: 'MP3',
  opus: 'Opus',
  vorbis: 'Vorbis',
};

/**
 * soundDetail describes a sound track of a stream through ffmpeg, such as
 * "Commentary · DTS 5.1, converted"; titled says to put the track's title
 * first, when its language names it.
 */
function soundDetail(a: StreamAudio, titled: boolean): string {
  const ch = a.channels || 0;
  const layout = ch === 1 ? 'mono' : ch === 2 ? 'stereo' : ch === 6 ? '5.1' : ch === 8 ? '7.1' : ch > 0 ? ch + ' channels' : '';
  const name = codecs[a.codec] || (a.codec.indexOf('pcm') === 0 ? 'PCM' : a.codec.toUpperCase());
  let d = name + (layout ? ' ' + layout : '') + (a.converted ? ', converted' : '');
  if (titled && a.title) {
    d = a.title + ' · ' + d;
  }
  return d;
}

function badge(text: string): string {
  return '<span class="badge">' + escape(text) + '</span>';
}

/** Names of the languages that sound and subtitle tracks are most often in, by ISO 639-2 and 639-1 code. */
const languages: { [code: string]: string } = {
  eng: 'English',
  fra: 'French',
  fre: 'French',
  deu: 'German',
  ger: 'German',
  spa: 'Spanish',
  ita: 'Italian',
  por: 'Portuguese',
  nld: 'Dutch',
  dut: 'Dutch',
  ara: 'Arabic',
  hin: 'Hindi',
  urd: 'Urdu',
  pan: 'Punjabi',
  tur: 'Turkish',
  pol: 'Polish',
  rus: 'Russian',
  ukr: 'Ukrainian',
  zho: 'Chinese',
  chi: 'Chinese',
  jpn: 'Japanese',
  kor: 'Korean',
  swe: 'Swedish',
  nor: 'Norwegian',
  dan: 'Danish',
  fin: 'Finnish',
  gre: 'Greek',
  ell: 'Greek',
  heb: 'Hebrew',
  cze: 'Czech',
  ces: 'Czech',
  slo: 'Slovak',
  slk: 'Slovak',
  hun: 'Hungarian',
  rum: 'Romanian',
  ron: 'Romanian',
  bul: 'Bulgarian',
  hrv: 'Croatian',
  srp: 'Serbian',
  slv: 'Slovenian',
  est: 'Estonian',
  lav: 'Latvian',
  lit: 'Lithuanian',
  ice: 'Icelandic',
  isl: 'Icelandic',
  tha: 'Thai',
  vie: 'Vietnamese',
  ind: 'Indonesian',
  may: 'Malay',
  msa: 'Malay',
  per: 'Persian',
  fas: 'Persian',
  tam: 'Tamil',
  tel: 'Telugu',
  ben: 'Bengali',
  qaa: 'Original',
  und: 'Unknown',
};

/** language names a language by its ISO 639 code, as sound and subtitle tracks give it; "" if not known. */
export function language(code: string): string {
  return languages[code.toLowerCase()] || '';
}
