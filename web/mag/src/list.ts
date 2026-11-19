// SPDX-License-Identifier: AGPL-3.0-or-later

// A list that shows a window of a longer list: it keeps one element per
// visible row and redraws them as the focus moves, so a lineup of
// thousands of channels costs no more than a screenful.

import { el, setClass } from './dom';

/** Draw fills in row for item index; it is not called for empty rows. */
export type Draw = (row: HTMLElement, index: number) => void;

export class ListView {
  private rows: HTMLElement[] = [];
  private rowClass: string;
  private draw: Draw;
  private count = 0;
  private current = 0;
  private top = 0;
  private active = false;

  /**
   * size rows of class rowClass go in parent; make builds each row's
   * contents once.
   */
  constructor(parent: HTMLElement, size: number, rowClass: string, make: (row: HTMLElement) => void, draw: Draw) {
    this.draw = draw;
    this.rowClass = rowClass;
    for (let i = 0; i < size; i++) {
      const row = el('div', rowClass, parent);
      make(row);
      this.rows.push(row);
    }
  }

  /** reset shows count items, with the focus on item focus. */
  reset(count: number, focus: number): void {
    this.count = count;
    this.top = 0;
    this.current = 0;
    this.moveTo(focus);
  }

  length(): number {
    return this.count;
  }

  /** focus returns the focused item. */
  focus(): number {
    return this.current;
  }

  /** first returns the first item shown. */
  first(): number {
    return this.top;
  }

  size(): number {
    return this.rows.length;
  }

  /** setActive says whether the list has the focus, which draws it stronger. */
  setActive(active: boolean): void {
    this.active = active;
    this.render();
  }

  /** move moves the focus by delta items, wrapping around the ends. */
  move(delta: number): void {
    if (this.count > 0) {
      this.moveTo((((this.current + delta) % this.count) + this.count) % this.count);
    }
  }

  /** page moves the focus by a screenful, stopping at the ends. */
  page(pages: number): void {
    this.moveTo(this.current + pages * this.rows.length);
  }

  /** moveTo moves the focus to item i, scrolling to keep it in view. */
  moveTo(i: number): void {
    const size = this.rows.length;
    this.current = Math.max(0, Math.min(i, this.count - 1));
    if (this.current < this.top) {
      this.top = this.current;
    } else if (this.current >= this.top + size) {
      this.top = this.current - size + 1;
    }
    this.top = Math.max(0, Math.min(this.top, this.count - size));
    this.render();
  }

  render(): void {
    for (let r = 0; r < this.rows.length; r++) {
      const row = this.rows[r]!;
      const i = this.top + r;
      if (i >= this.count) {
        setClass(row, this.rowClass + ' empty');
        continue;
      }
      let className = this.rowClass;
      if (i === this.current) {
        className += this.active ? ' focus' : ' selected';
      }
      setClass(row, className);
      this.draw(row, i);
    }
  }
}
