// SPDX-License-Identifier: AGPL-3.0-or-later

// Playback Check, from Settings (D70): the clips play in a window, and the
// viewer says after each whether it plays. Then the screen says what the
// server found.

import type { PlaybackCheck } from '../check';
import { el, setClass, setHTML, setText, setWidth, show } from '../dom';
import { icon } from '../icons';
import type { Press } from '../keys';
import { date } from '../time';
import { Clock, type Ctx, escape, Footer, type Hint, type Screen, type VideoArea } from './screen';

/** The questions the page asks once a clip plays. */
const questions = {
  picture: 'Do you see the picture moving?',
  sound: 'Do you hear the tone, with a beep each second?',
  both: 'Do you see the picture moving, and hear the tone?',
};

export class CheckScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private check: PlaybackCheck;

  private frame: HTMLElement;
  private plate: HTMLElement;
  private plateIcon: HTMLElement;
  private plateText: HTMLElement;
  private progress: HTMLElement;
  private progressFill: HTMLElement;
  private progressText: HTMLElement;
  private step: HTMLElement;
  private title: HTMLElement;
  private text: HTMLElement;
  private answers: HTMLElement;
  private detail: HTMLElement;
  private footer: Footer;
  private clockView: Clock;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.check = ctx.check;
    this.el = el('div', 'screen check', parent);
    const head = el('div', 'page-head', this.el);
    el('span', 'back', head, '‹ Settings');
    el('span', 'page-title', head, 'Playback Check');
    this.clockView = new Clock(head, false);

    this.frame = el('div', 'frame check-frame', this.el);
    this.plate = el('div', 'plate', this.frame);
    this.plateIcon = el('div', 'check-plate-icon', this.plate);
    this.plateText = el('div', 'plate-hint', this.plate);
    this.progress = el('div', 'check-progress', this.el);
    const bar = el('div', 'bar', this.progress);
    this.progressFill = el('div', 'fill acc', bar);
    this.progressText = el('div', 'check-progress-text', this.progress);

    const side = el('div', 'check-side', this.el);
    this.step = el('div', 'check-step', side);
    this.title = el('div', 'check-title', side);
    this.text = el('div', 'check-text', side);
    this.answers = el('div', 'check-answers', side);
    this.detail = el('div', 'check-detail', side);
    this.footer = new Footer(this.el);
  }

  enter(): void {
    this.check.stop();
    this.check.load();
    this.draw();
  }

  leave(): void {
    this.check.stop();
  }

  /** changed redraws the screen after the check moved on, and puts the video where it goes. */
  changed(): void {
    this.draw();
    this.ctx.videoChanged();
  }

  private draw(): void {
    const c = this.check;
    const running = c.phase === 'running';
    const clip = running ? c.clips[c.index] : undefined;
    let step = '';
    let title = '';
    let text = '';
    let answers = '';
    let detail = '';
    let plateText = '';
    let plateIcon = 'play';
    const left: Hint[] = [];
    let right: Hint[] = [{ key: 'Back', text: 'Settings' }];
    let pct = 0;
    let progress = '';

    switch (c.phase) {
      case 'idle':
        step = 'Playback Check';
        title = 'What this box plays';
        text =
          'Overscan plays short clips here, one after the other, to learn which kinds of film this box plays: their sound, their picture and their files. After each, say whether it plays. It takes about four minutes. Turn the sound up first.';
        detail = facts([
          ['Last checked', c.checked > 0 ? date(c.checked) : c.checked === 0 ? 'Never' : ''],
          ['Firmware', this.ctx.platform.firmware || 'Not known'],
        ]);
        plateText = 'The clips play here';
        right = [
          { key: 'OK', text: 'Start' },
          { key: 'Back', text: 'Settings' },
        ];
        break;
      case 'preparing':
        step = 'Playback Check';
        title = 'Preparing the clips';
        text = 'The server makes them with ffmpeg the first time, which takes about a minute.';
        plateIcon = 'server';
        plateText = c.total ? c.ready + ' of ' + c.total + ' made' : 'Asking the server';
        pct = c.total ? (100 * c.ready) / c.total : 0;
        progress = plateText;
        right = [{ key: 'Back', text: 'Stop' }];
        break;
      case 'unavailable':
        step = 'Playback Check';
        title = "The server can't make the clips";
        text = 'The check needs ffmpeg on the server, with its usual encoders. The server’s log says what went wrong.';
        plateIcon = 'server';
        break;
      case 'running': {
        const n = c.clips.length;
        step = 'Clip ' + (c.index + 1) + ' of ' + n;
        title = clip ? clip.label : '';
        if (c.clipPhase === 'skipping') {
          text = 'The box didn’t play it, so the check goes on without it.';
        } else if (clip) {
          // Asked at once: the viewer sees whether it plays before the box
          // says anything.
          text = questions[clip.ask] || questions.both;
          answers =
            answer('OK', 'Yes', 'yes') + answer('red', 'No', 'no') + answer('yellow', 'Not sure', 'unsure') + answer('l', 'Again', 'again');
          if (c.clipPhase === 'starting') {
            detail = '<div class="check-starting">Starting the clip…</div>';
          }
        }
        const done = c.answered();
        pct = (100 * c.index) / Math.max(1, n);
        progress = done.plays + (done.plays === 1 ? ' plays' : ' play') + ' · ' + done.not + (done.not === 1 ? ' doesn’t' : ' don’t');
        right = [{ key: 'Back', text: 'Stop' }];
        plateIcon = 'about';
        plateText = c.clipPhase === 'skipping' ? 'Not played' : '';
        break;
      }
      case 'saving':
        step = 'Playback Check';
        title = 'Saving what it found…';
        pct = 100;
        plateIcon = 'server';
        right = [];
        break;
      case 'done': {
        step = 'Playback Check';
        title = 'Done';
        const plays: string[] = [];
        const not: string[] = [];
        const found = c.saved ? c.saved.findings : [];
        for (let i = 0; i < found.length; i++) {
          (found[i]!.plays ? plays : not).push(found[i]!.label);
        }
        text = not.length
          ? 'From now on, a film whose sound or file this box doesn’t play goes through ffmpeg on the server.'
          : 'This box played every clip it was asked about.';
        detail =
          (plays.length ? '<div class="check-list"><span class="check-list-head">Plays</span>' + escape(plays.join(', ')) + '</div>' : '') +
          (not.length ? '<div class="check-list"><span class="check-list-head">Doesn’t play</span>' + escape(not.join(', ')) + '</div>' : '');
        pct = 100;
        plateIcon = 'check';
        plateText = 'Checked';
        right = [{ key: 'OK', text: 'Done' }];
        break;
      }
      case 'stopped':
        step = 'Playback Check';
        title = 'The first clip didn’t play';
        text =
          'Every box should play it: an MKV file with H.264 picture and AAC sound. Check that the TV shows the box and that its sound is on, then try again. Nothing was saved.';
        plateIcon = 'about';
        right = [
          { key: 'OK', text: 'Try Again' },
          { key: 'Back', text: 'Settings' },
        ];
        break;
      case 'failed':
        step = 'Playback Check';
        title = 'Something went wrong';
        text = c.error;
        plateIcon = 'about';
        right = [
          { key: 'OK', text: 'Try Again' },
          { key: 'Back', text: 'Settings' },
        ];
        break;
    }

    setText(this.step, step);
    setText(this.title, title);
    setText(this.text, text);
    setHTML(this.answers, answers);
    show(this.answers, answers !== '');
    setHTML(this.detail, detail);
    show(this.detail, detail !== '');
    const playing = this.check.playing();
    show(this.plate, !playing);
    setHTML(this.plateIcon, icon(plateIcon, 40, 1.6));
    setText(this.plateText, plateText);
    setClass(this.el, 'screen check' + (playing ? ' with-video' : ''));
    show(this.progress, c.phase !== 'idle' && c.phase !== 'unavailable' && c.phase !== 'stopped' && c.phase !== 'failed');
    setWidth(this.progressFill, pct);
    setText(this.progressText, progress);
    this.footer.set(left, right);
  }

  key(p: Press): boolean {
    const c = this.check;
    switch (c.phase) {
      case 'idle':
        if (p.key === 'ok') {
          this.ctx.player.stop();
          c.start();
          return true;
        }
        return false;
      case 'preparing':
      case 'saving':
        if (p.key === 'back') {
          c.stop();
          this.draw();
          return true;
        }
        return p.key === 'ok';
      case 'running':
        switch (p.key) {
          case 'ok':
          case 'green':
            c.answer('yes');
            return true;
          case 'red':
            c.answer('no');
            return true;
          case 'yellow':
            c.answer('');
            return true;
          case 'left':
            c.replay();
            return true;
          case 'back':
          case 'stop':
            c.stop();
            this.changed();
            return true;
        }
        // Nothing else leaves the check by mistake.
        return p.key !== 'menu' && p.key !== 'exit';
      case 'done':
        if (p.key === 'ok') {
          this.ctx.back();
          return true;
        }
        return false;
      case 'stopped':
      case 'failed':
        if (p.key === 'ok') {
          c.retry();
          return true;
        }
        return false;
    }
    return false;
  }

  tick(): void {
    this.clockView.tick();
  }

  lineupChanged(): void {}

  playback(): void {}

  video(): VideoArea {
    if (!this.check.playing()) {
      return 'none';
    }
    // The frame's place in the 1280×720 layout, in the page's pixels.
    const unit = parseFloat(document.documentElement.style.fontSize) || 16;
    return { x: 4 * unit, y: 7 * unit, w: 36 * unit, h: 20.25 * unit };
  }
}

/** answer writes one of the answers that the viewer can give, with its key. */
function answer(key: string, label: string, kind: string): string {
  const swatch = key === 'red' || key === 'yellow';
  const cap = swatch ? '<span class="swatch ' + key + '"></span>' : '<span class="keycap">' + (key === 'l' ? '◀' : escape(key)) + '</span>';
  return '<span class="check-answer ' + kind + '">' + cap + escape(label) + '</span>';
}

/** facts writes rows of a name and a value. */
function facts(rows: [string, string][]): string {
  let out = '';
  for (let i = 0; i < rows.length; i++) {
    out += '<div class="fact"><span>' + escape(rows[i]![0]) + '</span><span class="v">' + escape(rows[i]![1]) + '</span></div>';
  }
  return out;
}
