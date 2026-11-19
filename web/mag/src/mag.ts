// SPDX-License-Identifier: AGPL-3.0-or-later

// The MAG box, through its JavaScript API.

import {
  type AudioTrack,
  clamp,
  externalSubtitles,
  looseJSON,
  numberIn,
  type Platform,
  type PlayerEvent,
  type Rect,
} from './platform';

// The file in the box's user storage that keeps a copy of the device
// secret, which survives a restart (D38).
const secretFileName = 'overscan-secret.txt';

// Player events (Infomir's stbEvent reference).
const eventEnded = 1; // the end of the content, or a discontinuity in the stream
const eventTracks = 2; // what the stream's tracks are is known
const eventStarted = 4; // video or audio playback has begun
const eventFailed = 5; // the content could not be opened
const eventVideoInfo = 7; // the picture's size and rate are known
const eventSubtitlesFailed = 8; // subtitles from an address could not be loaded

/** The most of the player's events since the last play that the page keeps for the log. */
const maxRawEvents = 12;

/**
 * eventCode returns the code of a player event as the box gave it: a
 * number, or text such as "4"; NaN if it is neither.
 */
export function eventCode(event: unknown): number {
  if (typeof event === 'number') {
    return event;
  }
  return typeof event === 'string' && /^\s*\d+\s*$/.test(event) ? parseInt(event, 10) : NaN;
}

/** rawEvent writes an event as the box gave it, for the log: 4 for a number, "4" for text. */
export function rawEvent(event: unknown): string {
  if (typeof event === 'number') {
    return String(event);
  }
  if (typeof event === 'string') {
    return '"' + event.replace(/[^\x20-\x7e]/g, '?').substring(0, 8) + '"';
  }
  return typeof event;
}

/**
 * outputSize returns the size of the picture that the box sends in an
 * output mode such as 1080p-60, or null if the mode is not one it knows.
 */
export function outputSize(mode: string): { w: number; h: number } | null {
  const both = /(\d{3,4})x(\d{3,4})/.exec(mode);
  if (both) {
    return { w: parseInt(both[1]!, 10), h: parseInt(both[2]!, 10) };
  }
  const lines = /(2160|1080|720|576|480)[pi]/.exec(mode);
  const h = lines ? parseInt(lines[1]!, 10) : /PAL/i.test(mode) ? 576 : /NTSC/i.test(mode) ? 480 : 0;
  if (!h) {
    return null;
  }
  return { w: h > 576 ? Math.round((h * 16) / 9) : 720, h: h };
}

/** tracks returns the tracks that list gives, as [{pid:101, lang:["eng",""]}, ...]. */
function tracks(list: () => string): AudioTrack[] {
  let parsed: any = null;
  try {
    parsed = looseJSON(String(list() || ''));
  } catch (e) {
    parsed = null;
  }
  const out: AudioTrack[] = [];
  if (parsed && parsed.length) {
    for (let i = 0; i < parsed.length; i++) {
      const t = parsed[i];
      if (t && typeof t.pid === 'number') {
        const lang = t.lang && typeof t.lang === 'object' ? t.lang[0] : t.lang;
        out.push({ pid: t.pid, lang: typeof lang === 'string' ? lang : '' });
      }
    }
  }
  return out;
}

export function magPlatform(stb: STB): Platform {
  // The window the video shows in, null for full screen, applied again after
  // each Play in case the player resets it.
  let videoWindow: Rect | null = null;
  // The player's events since the last play, as the box gave them.
  let raw: string[] = [];

  function readOutputMode(): string {
    try {
      const env = looseJSON(stb.GetEnv('{"varList":["tvsystem"]}'));
      return (env && env.result && typeof env.result.tvsystem === 'string' && env.result.tvsystem) || '';
    } catch (e) {
      return '';
    }
  }
  const outputMode = readOutputMode();

  function readFirmware(): string {
    try {
      return stb.GetDeviceImageDesc ? String(stb.GetDeviceImageDesc() || '') : '';
    } catch (e) {
      return '';
    }
  }

  function applyWindow(): void {
    if (videoWindow) {
      // The box places the video in the pixels of its output mode, such as
      // 1920×1080, while it draws the page at its graphics resolution, such
      // as 1280×720 (D33), so the page's window is scaled from one to the
      // other.
      const r = videoWindow;
      const out = outputSize(outputMode);
      const sx = out ? out.w / (window.innerWidth || out.w) : 1;
      const sy = out ? out.h / (window.innerHeight || out.h) : 1;
      stb.SetPIG(0, 256, 0, 0);
      stb.SetViewport(Math.round(r.w * sx), Math.round(r.h * sy), Math.round(r.x * sx), Math.round(r.y * sy));
    } else {
      stb.SetPIG(1, 256, 0, 0);
    }
  }

  return {
    box: true,
    mac: stb.GetDeviceMacAddress(),
    model: stb.GetDeviceModelExt(),
    firmware: readFirmware(),
    outputMode: outputMode,

    secretFile: {
      load() {
        try {
          return stb.LoadUserData(secretFileName) || '';
        } catch (e) {
          return '';
        }
      },
      save(secret: string) {
        try {
          stb.SaveUserData(secretFileName, secret);
        } catch (e) {
          // The cookie still holds the secret.
        }
      },
    },

    init(listener: (event: PlayerEvent) => void) {
      // The page defines stbEvent for the box to call (Infomir's event
      // model tutorial); the box has none of its own.
      videoWindow = null;
      window.stbEvent = {
        event: 0,
        onEvent(event: number | string) {
          if (raw.length < maxRawEvents) {
            raw.push(rawEvent(event));
          }
          const code = eventCode(event);
          if (code === eventStarted) {
            listener('started');
          } else if (code === eventEnded) {
            listener('ended');
          } else if (code === eventFailed) {
            listener('failed');
          } else if (code === eventTracks || code === eventVideoInfo) {
            listener('info');
          } else if (code === eventSubtitlesFailed) {
            listener('subtitles');
          }
        },
      };
      stb.EnableSpatialNavigation(false); // the page moves the focus itself
      stb.InitPlayer();
      stb.SetVideoControl(0);
      stb.SetTopWin(0);
    },

    rawEvents() {
      return raw.join(' ');
    },

    play(solution: string, url: string) {
      raw = [];
      stb.Play(solution + ' ' + url);
      applyWindow();
    },

    stop() {
      stb.Stop();
    },

    isPlaying() {
      return stb.IsPlaying();
    },

    position() {
      try {
        const ms = stb.GetPosTimeEx ? stb.GetPosTimeEx() : 0;
        return ms > 0 ? ms / 1000 : stb.GetPosTime() || 0;
      } catch (e) {
        return 0;
      }
    },

    duration() {
      try {
        const ms = stb.GetMediaLenEx ? stb.GetMediaLenEx() : 0;
        return ms > 0 ? ms / 1000 : stb.GetMediaLen() || 0;
      } catch (e) {
        return 0;
      }
    },

    seek(sec: number) {
      if (stb.SetPosTimeEx) {
        stb.SetPosTimeEx(Math.max(0, Math.round(sec * 1000)));
      } else {
        stb.SetPosTime(Math.max(0, Math.round(sec)));
      }
    },

    pause() {
      stb.Pause();
    },

    resume() {
      stb.Continue();
    },

    setWindow(rect: Rect | null) {
      videoWindow = rect;
      applyWindow();
    },

    videoOnTop(on: boolean) {
      stb.SetTopWin(on ? 1 : 0);
    },

    videoInfo() {
      let text = '';
      try {
        text = String(stb.GetVideoInfo() || '');
      } catch (e) {
        text = '';
      }
      let fps = numberIn(text, 'frameRate');
      if (fps > 1000) {
        fps = fps / 1000; // in thousandths of a frame a second
      }
      return { width: numberIn(text, 'pictureWidth'), height: numberIn(text, 'pictureHeight'), fps: fps };
    },

    playerInfo() {
      let video = '';
      let audio = '';
      try {
        video = String(stb.GetVideoInfo() || '');
        audio = String((stb.GetAudioPIDsEx ? stb.GetAudioPIDsEx() : stb.GetAudioPIDs()) || '');
      } catch (e) {
        // What the player said before it failed.
      }
      return { video: video, audio: audio };
    },

    audioTracks() {
      return tracks(function () {
        return stb.GetAudioPIDs();
      });
    },

    audioTrack() {
      try {
        return stb.GetAudioPID() || 0;
      } catch (e) {
        return 0;
      }
    },

    setAudioTrack(pid: number) {
      stb.SetAudioPID(pid);
    },

    subtitleTracks() {
      return tracks(function () {
        return stb.GetSubtitlePIDs();
      });
    },

    showSubtitles(pid: number) {
      try {
        if (pid) {
          stb.SetSubtitlePID(pid);
        }
        stb.SetSubtitles(pid !== 0);
      } catch (e) {
        // A box without subtitles.
      }
    },

    loadSubtitles(url: string) {
      try {
        stb.SetSubtitlesEncoding('utf-8');
        stb.LoadExternalSubtitles(url);
        stb.SetSubtitlePID(externalSubtitles);
        stb.SetSubtitles(true);
      } catch (e) {
        // A box without subtitles.
      }
    },

    changeVolume(delta: number) {
      const volume = clamp(stb.GetVolume() + delta, 0, 100);
      stb.SetVolume(volume);
      if (stb.GetMute()) {
        stb.SetMute(0);
      }
      return volume;
    },

    toggleMute() {
      const mute = stb.GetMute() ? 0 : 1;
      stb.SetMute(mute);
      return mute === 1;
    },
  };
}
