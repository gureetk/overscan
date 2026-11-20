// SPDX-License-Identifier: AGPL-3.0-or-later

// What shows over any screen, and over the video: a short message, the
// channel number being typed, the PIN prompt, a list to pick from, and a
// message from the administrator.

import { el, setClass, setText, show } from './dom';
import { icon } from './icons';
import type { Press } from './keys';

/** A short message at the foot of the screen. */
export class Toast {
  private box: HTMLElement;
  private timer = 0;
  private changed: () => void;

  constructor(parent: HTMLElement, changed: () => void) {
    this.box = el('div', 'toast', parent);
    this.changed = changed;
    show(this.box, false);
  }

  visible(): boolean {
    return this.box.style.display !== 'none';
  }

  show(text: string): void {
    const self = this;
    setText(this.box, text);
    show(this.box, true);
    this.changed();
    clearTimeout(this.timer);
    this.timer = setTimeout(function () {
      show(self.box, false);
      self.changed();
    }, 4000);
  }
}

/** The channel number being typed, with the name of the channel it is. */
export class NumberBox {
  private box: HTMLElement;
  private digits: HTMLElement;
  private name: HTMLElement;

  constructor(parent: HTMLElement) {
    this.box = el('div', 'number-box', parent);
    this.digits = el('div', 'digits', this.box);
    this.name = el('div', 'name', this.box);
    show(this.box, false);
  }

  visible(): boolean {
    return this.box.style.display !== 'none';
  }

  show(digits: string, name: string): void {
    setText(this.digits, digits);
    setText(this.name, name);
    show(this.box, true);
  }

  hide(): void {
    show(this.box, false);
  }
}

/** A modal box that takes the keys while it shows. */
export interface Modal {
  visible(): boolean;
  key(p: Press): void;
}

/** The parental PIN prompt (D55). */
export class PinDialog implements Modal {
  private box: HTMLElement;
  private title: HTMLElement;
  private dots: HTMLElement[] = [];
  private message: HTMLElement;
  private pin = '';
  private done: ((pin: string | null) => void) | null = null;
  private changed: () => void;

  constructor(parent: HTMLElement, changed: () => void) {
    this.changed = changed;
    this.box = el('div', 'modal pin', parent);
    const panel = el('div', 'panel', this.box);
    el('div', 'pin-icon', panel).innerHTML = icon('lock', 40, 1.6);
    el('div', 'heading', panel, 'Enter the PIN');
    this.title = el('div', 'sub', panel);
    const row = el('div', 'dots', panel);
    for (let i = 0; i < 8; i++) {
      this.dots.push(el('span', 'dot', row));
    }
    this.message = el('div', 'message', panel);
    el('div', 'hint', panel, 'Type the PIN with the number keys, then press OK.');
    show(this.box, false);
  }

  visible(): boolean {
    return this.box.style.display !== 'none';
  }

  /** ask shows the prompt for what, with message, and calls done with the PIN or null. */
  ask(what: string, message: string, done: (pin: string | null) => void): void {
    this.pin = '';
    this.done = done;
    setText(this.title, what);
    setText(this.message, message);
    this.draw();
    show(this.box, true);
    this.changed();
  }

  key(p: Press): void {
    switch (p.key) {
      case 'digit':
        if (this.pin.length < 8) {
          this.pin += String(p.digit);
          setText(this.message, '');
        }
        break;
      case 'back':
      case 'left':
        if (this.pin === '') {
          this.finish(null);
          return;
        }
        this.pin = this.pin.slice(0, -1);
        break;
      case 'exit':
        this.finish(null);
        return;
      case 'ok':
        if (this.pin.length >= 4) {
          this.finish(this.pin);
          return;
        }
        setText(this.message, 'A PIN has 4 to 8 digits.');
        break;
    }
    this.draw();
  }

  private draw(): void {
    for (let i = 0; i < this.dots.length; i++) {
      // Show four places at first, and more as the PIN grows.
      setClass(this.dots[i]!, 'dot' + (i < this.pin.length ? ' on' : '') + (i >= Math.max(4, this.pin.length + 1) ? ' gone' : ''));
    }
  }

  private finish(pin: string | null): void {
    const done = this.done;
    this.done = null;
    show(this.box, false);
    this.changed();
    if (done) {
      done(pin);
    }
  }
}

/**
 * A message from the administrator (D96), shown as text, never as markup,
 * until the viewer closes it.
 */
export class MessageDialog implements Modal {
  private box: HTMLElement;
  private heading: HTMLElement;
  private text: HTMLElement;
  private sent: HTMLElement;
  private hint: HTMLElement;
  private done: (() => void) | null = null;
  private changed: () => void;

  constructor(parent: HTMLElement, changed: () => void) {
    this.changed = changed;
    this.box = el('div', 'modal notice', parent);
    const panel = el('div', 'panel', this.box);
    const head = el('div', 'heading', panel);
    el('span', 'notice-icon', head).innerHTML = icon('message', 26, 1.7);
    this.heading = el('span', '', head);
    this.text = el('div', 'text', panel);
    this.sent = el('div', 'sent', panel);
    this.hint = el('div', 'hint', panel);
    show(this.box, false);
  }

  visible(): boolean {
    return this.box.style.display !== 'none';
  }

  /** open shows a message, with when it was sent and how many wait after it, and calls done once it is closed. */
  open(text: string, sent: string, more: number, done: () => void): void {
    this.done = done;
    setText(this.heading, 'Message');
    setText(this.text, text);
    setText(this.sent, sent);
    setText(this.hint, more > 0 ? 'Press OK for the next one: ' + more + (more === 1 ? ' more waits.' : ' more wait.') : 'Press OK to close it.');
    show(this.box, true);
    this.changed();
  }

  key(p: Press): void {
    if (p.key === 'ok' || p.key === 'back' || p.key === 'exit') {
      const done = this.done;
      this.done = null;
      show(this.box, false);
      this.changed();
      if (done) {
        done();
      }
    }
  }
}

export interface PickItem {
  label: string;
  detail?: string;
}

/** A short list to pick one item from, such as a sound track or a group. */
export class Picker implements Modal {
  private box: HTMLElement;
  private title: HTMLElement;
  private list: HTMLElement;
  private rows: HTMLElement[] = [];
  private items: PickItem[] = [];
  private current = 0;
  private chosen = -1;
  private top = 0;
  private done: ((index: number) => void) | null = null;
  private changed: () => void;
  private static size = 9;

  constructor(parent: HTMLElement, changed: () => void) {
    this.changed = changed;
    this.box = el('div', 'modal picker', parent);
    const panel = el('div', 'panel', this.box);
    this.title = el('div', 'heading', panel);
    this.list = el('div', 'items', panel);
    for (let i = 0; i < Picker.size; i++) {
      const row = el('div', 'item', this.list);
      el('span', 'label', row);
      el('span', 'detail', row);
      this.rows.push(row);
    }
    show(this.box, false);
  }

  visible(): boolean {
    return this.box.style.display !== 'none';
  }

  /** pick shows items under title, with the focus on chosen, and calls done with the index picked, or -1. */
  pick(title: string, items: PickItem[], chosen: number, done: (index: number) => void): void {
    setText(this.title, title);
    this.items = items;
    this.chosen = chosen;
    this.current = Math.max(0, chosen);
    this.top = 0;
    this.done = done;
    this.draw();
    show(this.box, true);
    this.changed();
  }

  key(p: Press): void {
    const n = this.items.length;
    switch (p.key) {
      case 'up':
        this.current = (this.current - 1 + n) % n;
        break;
      case 'down':
        this.current = (this.current + 1) % n;
        break;
      case 'pageUp':
      case 'channelPrev':
        this.current = Math.max(0, this.current - Picker.size);
        break;
      case 'pageDown':
      case 'channelNext':
        this.current = Math.min(n - 1, this.current + Picker.size);
        break;
      case 'ok':
        this.finish(this.current);
        return;
      case 'back':
      case 'exit':
      case 'left':
        this.finish(-1);
        return;
    }
    this.draw();
  }

  private draw(): void {
    const size = Picker.size;
    if (this.current < this.top) {
      this.top = this.current;
    } else if (this.current >= this.top + size) {
      this.top = this.current - size + 1;
    }
    for (let r = 0; r < size; r++) {
      const row = this.rows[r]!;
      const i = this.top + r;
      const item = this.items[i];
      if (!item) {
        setClass(row, 'item empty');
        continue;
      }
      setClass(row, 'item' + (i === this.current ? ' focus' : '') + (i === this.chosen ? ' chosen' : ''));
      setText(row.firstChild as HTMLElement, item.label);
      setText(row.lastChild as HTMLElement, item.detail || '');
    }
  }

  private finish(index: number): void {
    const done = this.done;
    this.done = null;
    show(this.box, false);
    this.changed();
    if (done) {
      done(index);
    }
  }
}
