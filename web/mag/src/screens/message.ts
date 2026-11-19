// SPDX-License-Identifier: AGPL-3.0-or-later

// A message instead of the page: connecting, waiting for the
// administrator's approval, blocked, or no channels.

import { el, setText, show } from '../dom';
import type { Press } from '../keys';
import type { Screen, VideoArea } from './screen';

export interface MessageArg {
  title: string;
  text: string;
  /** A command or code to show in large type, if any. */
  detail?: string;
}

export class MessageScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private title: HTMLElement;
  private text: HTMLElement;
  private detail: HTMLElement;

  constructor(parent: HTMLElement) {
    this.el = el('div', 'screen message', parent);
    const brand = el('div', 'brand', this.el);
    el('span', 'mark', brand);
    el('span', 'word', brand, 'Overscan');
    const box = el('div', 'box', this.el);
    this.title = el('h1', 'title', box);
    this.text = el('p', 'text', box);
    this.detail = el('div', 'detail', box);
  }

  enter(arg: MessageArg): void {
    setText(this.title, arg.title);
    setText(this.text, arg.text);
    setText(this.detail, arg.detail || '');
    show(this.detail, !!arg.detail);
  }

  leave(): void {}

  key(_p: Press): boolean {
    return true;
  }

  tick(): void {}
  lineupChanged(): void {}
  playback(): void {}

  video(): VideoArea {
    return 'none';
  }
}
