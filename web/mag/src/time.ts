// SPDX-License-Identifier: AGPL-3.0-or-later

// The page's clock and how it writes times. A box's clock can be wrong, so
// the page takes the time from the server's guide answers (docs/api/
// device.md) and keeps the difference. Times show in the box's own time
// zone, on a 24- or 12-hour clock as the box's settings say. The box has
// no Intl, so names and formats are the page's own.

let offsetMs = 0;
let twelveHour = false;

const days = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const months = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** now returns the time, by the server's clock, in Unix seconds. */
export function now(): number {
  return (new Date().getTime() + offsetMs) / 1000;
}

/** serverTime takes now from the server: its time, in Unix seconds, when it answered. */
export function serverTime(sec: number): void {
  if (sec > 0) {
    offsetMs = sec * 1000 - new Date().getTime();
  }
}

/** useTwelveHour chooses the 12-hour clock, or the 24-hour one. */
export function useTwelveHour(on: boolean): void {
  twelveHour = on;
}

function pad(n: number): string {
  return n < 10 ? '0' + n : String(n);
}

function local(sec: number): Date {
  return new Date(sec * 1000);
}

/** clock writes a time of day: 20:41, or 8:41 pm. */
export function clock(sec: number): string {
  const d = local(sec);
  if (!twelveHour) {
    return pad(d.getHours()) + ':' + pad(d.getMinutes());
  }
  const h = d.getHours() % 12 || 12;
  return h + ':' + pad(d.getMinutes()) + (d.getHours() < 12 ? ' am' : ' pm');
}

/** span writes a programme's times: 19:45–22:00, or 7:45–10:00 pm. */
export function span(start: number, stop: number): string {
  if (!twelveHour) {
    return clock(start) + '–' + clock(stop);
  }
  const a = clock(start);
  const b = clock(stop);
  const sameHalf = a.slice(-2) === b.slice(-2);
  return (sameHalf ? a.slice(0, -3) : a) + '–' + b;
}

/** startOfDay returns the box's local midnight on the day of sec. */
export function startOfDay(sec: number): number {
  const d = local(sec);
  d.setHours(0, 0, 0, 0);
  return d.getTime() / 1000;
}

/** date writes a date: Wed 21 Oct. */
export function date(sec: number): string {
  const d = local(sec);
  return days[d.getDay()] + ' ' + d.getDate() + ' ' + months[d.getMonth()];
}

/** fullDate writes a date with its year: 31 Dec 2027. */
export function fullDate(sec: number): string {
  const d = local(sec);
  return d.getDate() + ' ' + months[d.getMonth()] + ' ' + d.getFullYear();
}

/** day writes a day as near to today: Today, Tomorrow, Yesterday or Thu 22. */
export function day(sec: number): string {
  const today = startOfDay(now());
  const diff = Math.round((startOfDay(sec) - today) / 86400);
  if (diff === 0) {
    return 'Today';
  } else if (diff === 1) {
    return 'Tomorrow';
  } else if (diff === -1) {
    return 'Yesterday';
  }
  const d = local(sec);
  return days[d.getDay()] + ' ' + d.getDate();
}

/** duration writes a length of time: 1 h 19 min, 45 min, or 2 h. */
export function duration(sec: number): string {
  const min = Math.max(1, Math.round(sec / 60));
  const h = Math.floor(min / 60);
  const m = min % 60;
  if (h === 0) {
    return m + ' min';
  }
  return m === 0 ? h + ' h' : h + ' h ' + m + ' min';
}

/** progress returns how far now is from start to stop, 0 to 100. */
export function progress(start: number, stop: number, at: number): number {
  if (stop <= start) {
    return 0;
  }
  return Math.max(0, Math.min(100, ((at - start) / (stop - start)) * 100));
}

/** hms writes a place in a film: 1:02:13, or 12:30. */
export function hms(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return (h > 0 ? h + ':' + pad(m) : String(m)) + ':' + pad(s % 60);
}
