// SPDX-License-Identifier: AGPL-3.0-or-later

// Recording on the server from the box (D89): a programme in the guide, or
// what is on now on a channel in Live TV or full screen. The same key calls
// a programme's recording off.

import { type Channel, describe, type Programme } from '../api';
import { keptPIN } from '../pin';
import { label } from '../player';
import { clock, now, onDay } from '../time';
import type { Ctx } from './screen';

/** recordable says whether the box can offer to record p now: the server records, and p has not ended. */
export function recordable(ctx: Ctx, p: Programme | null): boolean {
  return ctx.recordings.on && !!p && p.stop > now();
}

/** recordText says what the record key does to p: record it, or call its recording off. */
export function recordText(ctx: Ctx, c: Channel, p: Programme): string {
  return ctx.recordings.find(c.id, p.start) ? 'Cancel Recording' : 'Record';
}

/**
 * record records the programme p of channel c, which has not ended, or
 * calls its recording off. A locked channel's needs the parental PIN. done
 * runs once the recordings changed.
 */
export function record(ctx: Ctx, c: Channel, p: Programme | null, done: () => void): void {
  const recordings = ctx.recordings;
  if (!recordings.on) {
    ctx.toast(describe({ status: 409, code: 'recording_off', retryAfter: 0 }));
    return;
  }
  if (!p) {
    ctx.toast('The guide shows nothing on ' + c.name + ' now, so there is nothing to record.');
    return;
  }
  if (p.stop <= now()) {
    ctx.toast(p.title + ' has ended, so it cannot be recorded.');
    return;
  }
  const r = recordings.find(c.id, p.start);
  if (r) {
    recordings.cancel(r, function (err) {
      ctx.toast(err ? "Couldn't cancel the recording. " + describe(err) : 'Cancelled the recording of ' + p.title + '.');
      done();
    });
    return;
  }
  const schedule = function (pin: string) {
    recordings.record(c.id, p.start, pin, function (err) {
      if (err) {
        ctx.toast("Couldn't record " + p.title + '. ' + describe(err));
      } else if (p.start <= now()) {
        ctx.toast('Recording ' + p.title + ' until ' + clock(p.stop) + '. It will be on Movies.');
      } else {
        ctx.toast(p.title + ' will be recorded at ' + clock(p.start) + ' ' + onDay(p.start) + '. It will be on Movies.');
      }
      done();
    });
  };
  if (c.locked) {
    ctx.unlock(label(c), function (ok) {
      if (ok) {
        schedule(keptPIN());
      }
    });
    return;
  }
  schedule('');
}
