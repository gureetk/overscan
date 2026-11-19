// SPDX-License-Identifier: AGPL-3.0-or-later

// A few DOM helpers. The page builds its elements once and then changes
// only their text, classes and images, which the box's slow CPU handles
// best.

/** el makes an element with a class, adds it to parent and returns it. */
export function el(tag: string, className: string, parent: HTMLElement | null, text?: string): HTMLElement {
  const e = document.createElement(tag);
  e.className = className;
  if (text) {
    e.textContent = text;
  }
  if (parent) {
    parent.appendChild(e);
  }
  return e;
}

/** setText sets e's text, touching the DOM only if it changes. */
export function setText(e: HTMLElement, text: string): void {
  if (e.textContent !== text) {
    e.textContent = text;
  }
}

/** setClass sets e's class attribute, touching the DOM only if it changes. */
export function setClass(e: HTMLElement, className: string): void {
  if (e.className !== className) {
    e.className = className;
  }
}

/** toggle adds or removes one class of e. */
export function toggle(e: HTMLElement, name: string, on: boolean): void {
  const has = (' ' + e.className + ' ').indexOf(' ' + name + ' ') >= 0;
  if (on && !has) {
    e.className = e.className ? e.className + ' ' + name : name;
  } else if (!on && has) {
    e.className = (' ' + e.className + ' ').replace(' ' + name + ' ', ' ').replace(/^\s+|\s+$/g, '');
  }
}

/** show shows or hides e. */
export function show(e: HTMLElement, on: boolean): void {
  const display = on ? '' : 'none';
  if (e.style.display !== display) {
    e.style.display = display;
  }
}

/** setWidth sets e's width as a percentage, 0 to 100. */
export function setWidth(e: HTMLElement, pct: number): void {
  const w = Math.max(0, Math.min(100, Math.round(pct * 10) / 10)) + '%';
  if (e.style.width !== w) {
    e.style.width = w;
  }
}

/** setHTML sets e's markup, touching the DOM only if it changes. */
export function setHTML(e: HTMLElement, html: string): void {
  if (e.getAttribute('data-html') !== html) {
    e.setAttribute('data-html', html);
    e.innerHTML = html;
  }
}

/**
 * Logo shows a channel's logo on a plate, or its name where it has none or
 * the logo does not load.
 */
export class Logo {
  readonly el: HTMLElement;
  private img: HTMLImageElement;
  private name: HTMLElement;
  private src = '';
  private failed = false;

  constructor(className: string, parent: HTMLElement) {
    const self = this;
    this.el = el('span', 'logo ' + className, parent);
    this.img = el('img', '', this.el) as HTMLImageElement;
    this.img.alt = '';
    this.name = el('span', 'logo-name', this.el);
    this.img.onerror = function () {
      self.failed = true;
      self.update();
    };
    this.img.onload = function () {
      self.failed = false;
      self.update();
    };
    show(this.img, false);
  }

  /** set shows src, a logo's address, or name when src is "". */
  set(src: string, name: string): void {
    setText(this.name, shortName(name));
    if (src !== this.src) {
      this.src = src;
      this.failed = false;
      if (src) {
        this.img.src = src;
      } else {
        this.img.removeAttribute('src');
      }
    }
    this.update();
  }

  private update(): void {
    const image = this.src !== '' && !this.failed && this.img.complete && this.img.naturalWidth > 0;
    show(this.img, image);
    show(this.name, !image);
  }
}

/**
 * shortName shortens a channel's name for a logo plate: without a country
 * prefix such as "UK: " and without quality tags.
 */
export function shortName(name: string): string {
  const s = name
    .replace(/^\s*[A-Z]{2,3}\s*[:|]\s*/, '')
    .replace(/\s+\b(F?HD|SD|UHD|4K|HEVC|H\.?265)\b/gi, '')
    .replace(/\s+/g, ' ')
    .replace(/^\s+|\s+$/g, '');
  return s || name;
}
