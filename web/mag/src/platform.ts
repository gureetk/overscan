// SPDX-License-Identifier: AGPL-3.0-or-later

// What the page needs from the box, which dev mode stands in for in a
// desktop browser.

/** What the player reports: subtitles, that subtitles from an address could not be loaded. */
export type PlayerEvent = 'started' | 'ended' | 'failed' | 'info' | 'subtitles';

/** The pid that the box gives subtitles loaded from an address. */
export const externalSubtitles = 0x2000;

/** A file in the box's own storage, for a copy of the device secret. */
export interface SecretFile {
  load(): string;
  save(secret: string): void;
}

/** A rectangle on the page, in CSS pixels. */
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** What the player says about the picture, 0 where it does not know. */
export interface VideoInfo {
  width: number;
  height: number;
  /** Frames a second. */
  fps: number;
}

/** A sound track of the stream that plays. */
export interface AudioTrack {
  pid: number;
  /** An ISO 639 code, such as "eng", or "". */
  lang: string;
}

export interface Platform {
  /** False in dev mode. */
  box: boolean;
  mac: string;
  model: string;
  /** The box's firmware version, such as 2.20.16-322; "" if not known. */
  firmware: string;
  /** Where the box keeps a copy of its secret besides the cookie, if anywhere. */
  secretFile: SecretFile | null;
  /** The video output mode, such as 1080p-60, or "" if not known. */
  outputMode: string;
  /** Prepares the player and the screen, and sends player events to listener. */
  init(listener: (event: PlayerEvent) => void): void;
  /**
   * The player's events since the last play, as the box gave them, for the
   * server's log: 4 2 7, or "4" "2" "7" when it gives them as text.
   */
  rawEvents(): string;
  /** Plays a link: solution is the box's kind of player, such as ffrt. */
  play(solution: string, url: string): void;
  stop(): void;
  isPlaying(): boolean;
  /** Where the file that plays is, and how long it is, in seconds; 0 where the box does not know. */
  position(): number;
  duration(): number;
  /** Moves the file that plays to a time, in seconds. */
  seek(sec: number): void;
  pause(): void;
  resume(): void;
  /** Shows the video full screen (null) or in a window on the page. */
  setWindow(rect: Rect | null): void;
  /** Shows the video over the page, or the page over the video. */
  videoOnTop(on: boolean): void;
  videoInfo(): VideoInfo;
  /** What the player says of the picture and of the sound tracks of what plays, as it says it. */
  playerInfo(): { video: string; audio: string };
  audioTracks(): AudioTrack[];
  /** The pid of the sound track that plays, or 0. */
  audioTrack(): number;
  setAudioTrack(pid: number): void;
  /** The subtitle tracks of the stream that plays, as the box lists them. */
  subtitleTracks(): AudioTrack[];
  /** Shows the subtitle track with a pid, or none with 0. */
  showSubtitles(pid: number): void;
  /** Loads subtitles in UTF-8 SRT from an address and shows them. */
  loadSubtitles(url: string): void;
  /** Changes the volume by delta, unmuting, and returns the new volume, 0 to 100. */
  changeVolume(delta: number): number;
  /** Mutes or unmutes, and returns whether the sound is now off. */
  toggleMute(): boolean;
}

/** clamp returns n limited to [lo, hi]. */
export function clamp(n: number, lo: number, hi: number): number {
  return n < lo ? lo : n > hi ? hi : n;
}

/**
 * looseJSON parses what the box's API returns as JSON with unquoted keys,
 * such as {pid:101, lang:["eng",""]}, or returns null.
 */
export function looseJSON(text: string): any {
  if (!text) {
    return null;
  }
  try {
    return JSON.parse(text);
  } catch (e) {
    // Quote the keys and try again.
  }
  try {
    return JSON.parse(text.replace(/([{,]\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:/g, '$1"$2":'));
  } catch (e) {
    return null;
  }
}

/** numberIn returns the number after name in text, such as frameRate:50000, or 0. */
export function numberIn(text: string, name: string): number {
  const m = new RegExp(name + '"?\\s*:\\s*"?(\\d+(?:\\.\\d+)?)').exec(text);
  return m ? parseFloat(m[1]!) : 0;
}
