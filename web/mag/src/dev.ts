// SPDX-License-Identifier: AGPL-3.0-or-later

// Dev mode: a desktop browser standing in for the box, to work on the page
// without one. A panel stands in for the video, full screen or in a window,
// under the page or over it as on the box, and says what the box would
// play.

import { clamp, type Platform, type PlayerEvent, type Rect } from './platform';

/**
 * devPlatform stands in for the box. Addresses that fail matches cannot be
 * opened; those that hold matches stand still, without a word from the
 * player, until they are paused and go on again, as some films did on a
 * box.
 */
export function devPlatform(mac: string, fail?: RegExp, hold?: RegExp): Platform {
  let listener: (event: PlayerEvent) => void = function () {};
  let playing = false;
  // Whether what plays stands still until a pause and a go-on.
  let held = false;
  let events: string[] = [];
  let volume = 50;
  let muted = false;
  let timer = 0;
  let track = 101;
  // A file that plays: where it started, in seconds, and when, and where a
  // pause stopped it.
  let file = false;
  let from = 0;
  let since = 0;
  let pausedAt = -1;
  const fullLength = 6000;
  // How long what plays is: the file, or what is left of it from where a
  // stream through ffmpeg starts.
  let fileLength = fullLength;

  function clock(): number {
    return new Date().getTime() / 1000;
  }

  function where(): number {
    if (!file) {
      return 0;
    }
    if (held) {
      return from;
    }
    return Math.min(fileLength, pausedAt >= 0 ? pausedAt : from + (clock() - since));
  }

  function tell(event: PlayerEvent): void {
    events.push(event);
    listener(event);
  }

  // A file ends, as the box says with its event, when it plays to its length.
  let endTimer = 0;
  function scheduleEnd(): void {
    clearTimeout(endTimer);
    if (file && pausedAt < 0 && !held) {
      endTimer = setTimeout(function () {
        playing = false;
        tell('ended');
      }, (fileLength - where()) * 1000);
    }
  }

  const video = document.createElement('div');
  video.className = 'dev-video';
  const label = document.createElement('div');
  video.appendChild(label);
  // What subtitles the box would show.
  const subtitles = document.createElement('div');
  video.appendChild(subtitles);

  function place(rect: Rect | null): void {
    const s = video.style;
    if (rect) {
      s.left = rect.x + 'px';
      s.top = rect.y + 'px';
      s.width = rect.w + 'px';
      s.height = rect.h + 'px';
      s.right = s.bottom = 'auto';
    } else {
      s.left = s.top = s.right = s.bottom = '0';
      s.width = s.height = 'auto';
    }
  }

  return {
    box: false,
    mac: mac,
    model: 'dev',
    firmware: 'dev',
    outputMode: '',
    secretFile: null, // the cookie is enough in a desktop browser

    init(l: (event: PlayerEvent) => void) {
      listener = l;
      document.body.insertBefore(video, document.body.firstChild);
      place(null);
      video.style.display = 'none';
    },

    rawEvents() {
      return events.join(' ');
    },

    play(solution: string, url: string) {
      playing = true;
      events = [];
      label.textContent = 'The box would play: ' + solution + ' ' + url;
      video.style.display = 'block';
      // A link to a file through the hub: it can seek, and starts where
      // position: says. A stream that ffmpeg makes of one from a time
      // (?t=) is what is left of the file from there, and the box counts
      // its positions from 0.
      file = url.indexOf('/vod/') >= 0 || url.indexOf('/archive/') >= 0;
      const at = /\bposition:(\d+)/.exec(url);
      const t = /[?&]t=(\d+(?:\.\d+)?)/.exec(url);
      fileLength = Math.max(1, fullLength - (t ? parseFloat(t[1]!) : 0));
      from = at ? parseInt(at[1]!, 10) : 0;
      since = clock();
      pausedAt = -1;
      clearTimeout(timer);
      // An address that dev mode is told to fail, with ?fail=, cannot be
      // opened, as the box says with its event.
      const failed = !!fail && fail.test(url);
      held = !failed && !!hold && hold.test(url);
      label.textContent += held ? ' (standing still until paused and played again)' : '';
      timer = setTimeout(function () {
        if (failed) {
          playing = false;
          tell('failed');
        } else if (!held) {
          tell('started');
        }
      }, 300);
      scheduleEnd();
    },

    stop() {
      playing = false;
      held = false;
      file = false;
      clearTimeout(timer);
      clearTimeout(endTimer);
      label.textContent = '';
      subtitles.textContent = '';
      video.style.display = 'none';
    },

    isPlaying() {
      return playing;
    },

    position() {
      return where();
    },

    duration() {
      return file ? fileLength : 0;
    },

    seek(sec: number) {
      from = Math.max(0, Math.min(fileLength, sec));
      since = clock();
      if (pausedAt >= 0) {
        pausedAt = from;
      }
      scheduleEnd();
    },

    pause() {
      pausedAt = where();
      clearTimeout(endTimer);
    },

    resume() {
      if (pausedAt >= 0) {
        from = pausedAt;
        since = clock();
        pausedAt = -1;
        if (held) {
          held = false;
          tell('started');
        }
      }
      scheduleEnd();
    },

    setWindow(rect: Rect | null) {
      place(rect);
    },

    videoOnTop(on: boolean) {
      video.className = on ? 'dev-video dev-video-top' : 'dev-video';
    },

    videoInfo() {
      return playing ? { width: 1920, height: 1080, fps: 50 } : { width: 0, height: 0, fps: 0 };
    },

    playerInfo() {
      return playing
        ? { video: '{frameRate:25000,pictureWidth:1920,pictureHeight:1080}', audio: '[{pid:101,lang:["eng"]},{pid:102,lang:["fra"]}]' }
        : { video: '', audio: '' };
    },

    audioTracks() {
      return playing
        ? [
            { pid: 101, lang: 'eng' },
            { pid: 102, lang: 'fra' },
          ]
        : [];
    },

    audioTrack() {
      return playing ? track : 0;
    },

    setAudioTrack(pid: number) {
      track = pid;
    },

    subtitleTracks() {
      // A file as it is has a track of its own; a stream through ffmpeg
      // has none.
      return playing && file && fileLength === fullLength ? [{ pid: 201, lang: 'eng' }] : [];
    },

    showSubtitles(pid: number) {
      subtitles.textContent = pid ? 'Subtitles: the track with pid ' + pid : '';
    },

    loadSubtitles(url: string) {
      subtitles.textContent = 'Subtitles: ' + url;
    },

    changeVolume(delta: number) {
      volume = clamp(volume + delta, 0, 100);
      muted = false;
      return volume;
    },

    toggleMute() {
      muted = !muted;
      return muted;
    },
  };
}
