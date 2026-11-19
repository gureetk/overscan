// SPDX-License-Identifier: AGPL-3.0-or-later

// The parts of the MAG JavaScript API that the page uses, as Infomir's
// reference documents them (https://soft.infomir.com/stbapi/JS/v343/).

/** The box: gSTB. */
interface STB {
  GetDeviceMacAddress(): string;
  GetDeviceModelExt(): string;
  /** The firmware's version, such as 2.20.16-322. */
  GetDeviceImageDesc?(): string;
  /** Turns the browser's own arrow-key navigation on or off. */
  EnableSpatialNavigation(enable: boolean): void;
  /** Prepares the player; called before its first use. */
  InitPlayer(): void;
  /** 0: the box shows the video window while something plays. */
  SetVideoControl(mode: number): void;
  /** Puts a window over the others: 0 the page, 1 the video. */
  SetTopWin(window: number): void;
  /** state 1 shows the video full screen; scale is in 256ths. */
  SetPIG(state: number, scale: number, x: number, y: number): void;
  /** Shows the video in a window: its width, height and top-left corner, in pixels. */
  SetViewport(width: number, height: number, x: number, y: number): void;
  /** The picture's size and frame rate, as {frameRate:50000,pictureWidth:1920,...}. */
  GetVideoInfo(): string;
  /** The stream's sound tracks, as [{pid:101,lang:["eng",""]},...]. */
  GetAudioPIDs(): string;
  /** The same, with more of what the box knows of each, where it has it. */
  GetAudioPIDsEx?(): string;
  GetAudioPID(): number;
  SetAudioPID(pid: number): void;
  /** The stream's subtitle tracks, in the same form as GetAudioPIDs; an external one has pid 0x2000. */
  GetSubtitlePIDs(): string;
  /** Picks the subtitle track with a pid; none if there is no such track. */
  SetSubtitlePID(pid: number): void;
  /** Shows the subtitles, or hides them. */
  SetSubtitles(on: boolean): void;
  /** Loads subtitles from an address, such as an SRT file, as the track with pid 0x2000; event 8 says it failed. */
  LoadExternalSubtitles(url: string): void;
  /** The character set of subtitles loaded from an address, such as utf-8. */
  SetSubtitlesEncoding(encoding: string): void;
  /** Boot loader variables, asked for as {"varList":["tvsystem"]}; answers JSON. */
  GetEnv(query: string): string;
  /** Plays "solution url", such as "ffrt http://...", with options after it such as position:120. */
  Play(command: string): void;
  Stop(): void;
  IsPlaying(): boolean;
  /** Pauses what plays; Continue goes on from there. */
  Pause(): void;
  Continue(): void;
  /** Where playback is, in seconds from the start of what plays. */
  GetPosTime(): number;
  /** The same in milliseconds, where the box has it. */
  GetPosTimeEx?(): number;
  /** Moves playback to a time, in seconds from the start. */
  SetPosTime(sec: number): void;
  /** The same in milliseconds, where the box has it. */
  SetPosTimeEx?(ms: number): void;
  /** How long what plays is, in seconds; 0 if not known. */
  GetMediaLen(): number;
  /** The same in milliseconds, where the box has it. */
  GetMediaLenEx?(): number;
  /** 0 to 100. */
  GetVolume(): number;
  SetVolume(volume: number): void;
  /** 1 when muted. */
  GetMute(): number;
  SetMute(mute: number): void;
  /** Reads a file from the box's user storage; "" if there is none. */
  LoadUserData(name: string): string;
  /** Writes a file to the box's user storage. */
  SaveUserData(name: string, data: string): void;
}

/**
 * What the page defines for the box to call with events. The reference
 * gives the event's code as a number; a box may give it as text, such as
 * "4", so the page reads either.
 */
interface STBEvents {
  onEvent(event: number | string, info?: string): void;
  event: number;
}

interface Window {
  gSTB?: STB;
  stbEvent?: STBEvents;
}
