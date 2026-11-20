// SPDX-License-Identifier: AGPL-3.0-or-later

// Recording on the server from the box (D89, D98): a programme in the
// guide, or a channel from now on in Live TV and full screen, with the
// green key or REC. The same key stops a recording under way, which keeps
// what it recorded, or calls off one to come.

import { type Channel, describe, type Programme } from '../api';
import { keptPIN } from '../pin';
import { label } from '../player';
import { type Recording, recordingNow } from '../recordings';
import { clock, now, onDay } from '../time';
import type { Ctx } from './screen';

/** recordable says whether the guide can offer to record p now: the server records, and p has not ended. */
export function recordable(ctx: Ctx, p: Programme | null): boolean {
  return ctx.recordings.on && !!p && p.stop > now();
}

/** recordText says what the record key does to programme p of channel c: record it, stop its recording, or call it off. */
export function recordText(ctx: Ctx, c: Channel, p: Programme): string {
  const r = ctx.recordings.forProgramme(c.id, p.start, p.stop);
  return r ? (recordingNow(r, now()) ? 'Stop Recording' : 'Cancel Recording') : 'Record';
}

/** channelText says what the record key does on channel c now: record it, or stop its recording. */
export function channelText(ctx: Ctx, c: Channel): string {
  return ctx.recordings.onChannel(c.id, now()) ? 'Stop Recording' : 'Record';
}

/**
 * record records the programme p of channel c, which has not ended, or
 * stops or calls off its recording. done runs once the recordings changed.
 */
export function record(ctx: Ctx, c: Channel, p: Programme | null, done: () => void): void {
  const recordings = ctx.recordings;
  if (!recordings.on) {
    ctx.toast(describe({ status: 409, code: 'recording_off', retryAfter: 0 }));
    return;
  }
  if (!p) {
    recordChannel(ctx, c, done);
    return;
  }
  if (p.stop <= now()) {
    ctx.toast(p.title + ' has ended, so it cannot be recorded.');
    return;
  }
  const r = recordings.forProgramme(c.id, p.start, p.stop);
  if (r) {
    stop(ctx, r, done);
    return;
  }
  withPIN(ctx, c, function (pin) {
    recordings.record(c.id, p.start, pin, function (err, got) {
      if (err || !got) {
        ctx.toast("Couldn't record " + p.title + '. ' + describe(err));
      } else if (got.start <= now()) {
        ctx.toast('Recording ' + p.title + ' until ' + clock(p.stop) + '. It will be on Movies.');
      } else {
        ctx.toast(p.title + ' will be recorded at ' + clock(p.start) + ' ' + onDay(p.start) + '. It will be on Movies.');
      }
      done();
    });
  });
}

/**
 * recordChannel records channel c from now on, until the end of the
 * programme the guide shows on it or until it is stopped, as the server
 * says; or it stops the channel's recording under way. done runs once the
 * recordings changed.
 */
export function recordChannel(ctx: Ctx, c: Channel, done: () => void): void {
  const recordings = ctx.recordings;
  if (!recordings.on) {
    ctx.toast(describe({ status: 409, code: 'recording_off', retryAfter: 0 }));
    return;
  }
  const r = recordings.onChannel(c.id, now());
  if (r) {
    stop(ctx, r, done);
    return;
  }
  withPIN(ctx, c, function (pin) {
    recordings.recordNow(c.id, pin, function (err, got) {
      if (err || !got) {
        ctx.toast("Couldn't record " + c.name + '. ' + describe(err));
      } else if (got.until_stopped) {
        ctx.toast('Recording ' + got.title + ' until you stop it, or until ' + clock(got.stop) + ' at the latest. It will be on Movies.');
      } else {
        ctx.toast('Recording ' + got.title + ' until ' + clock(got.stop) + '. It will be on Movies.');
      }
      done();
    });
  });
}

/** stop stops a recording under way, which keeps what it recorded, or calls off one to come. */
export function stop(ctx: Ctx, r: Recording, done: () => void): void {
  const running = recordingNow(r, now());
  ctx.recordings.cancel(r, function (err) {
    if (err) {
      ctx.toast((running ? "Couldn't stop the recording. " : "Couldn't cancel the recording. ") + describe(err));
    } else if (running) {
      ctx.toast('Stopped recording ' + r.title + '. What it recorded is on Movies.');
    } else {
      ctx.toast('Cancelled the recording of ' + r.title + '.');
    }
    done();
  });
}

/** withPIN asks for the parental PIN for a locked channel, then goes on. */
function withPIN(ctx: Ctx, c: Channel, go: (pin: string) => void): void {
  if (c.locked) {
    ctx.unlock(label(c), function (ok) {
      if (ok) {
        go(keptPIN());
      }
    });
    return;
  }
  go('');
}
