// SPDX-License-Identifier: AGPL-3.0-or-later

// The device API (docs/api/device.md), over XMLHttpRequest: the box has no
// fetch, and its XMLHttpRequest has no json response type.

export interface ApiError {
  /** The HTTP status, or 0 when the server did not answer. */
  status: number;
  /** The API's error code, or network, timeout or http_<status>. */
  code: string;
  /** Seconds to wait, from Retry-After, or 0. */
  retryAfter: number;
}

export type Callback<T> = (err: ApiError | null, data: T | null) => void;

export interface Session {
  state: 'approved' | 'pending' | 'blocked';
  mac: string;
  /** The code of the box's request to pair, while it waits. */
  code?: string;
  /** A new secret for the box to keep. */
  secret?: string;
  token?: string;
  expires?: number;
  label?: string;
  version: string;
}

export interface Group {
  id: number;
  name: string;
}

export interface Channel {
  id: number;
  number: number;
  name: string;
  /** 0 for no group. */
  group: number;
  /** The path of its logo on Overscan, if it has one. */
  logo?: string;
  /** Playing it needs the account's parental PIN. */
  locked?: boolean;
  /** How many days back its past programmes play from its source's archive (D81); 0 or missing for none. */
  archive?: number;
  /** It is a radio station (D87). */
  radio?: boolean;
}

export interface Lineup {
  version: string;
  groups: Group[];
  channels: Channel[];
  /** Radio stations show on the Radio screen alone, and not among the channels (D87). */
  radio_apart?: boolean;
  favourites: number[];
  recent: number[];
}

export interface PlayLink {
  url: string;
  solution: string;
}

/** A sound track of a stream that ffmpeg makes of a film (D68). */
export interface StreamAudio {
  /** As the file tags it, such as eng. */
  language?: string;
  title?: string;
  /** The file's codec, as ffmpeg names it, such as dts. */
  codec: string;
  channels?: number;
  /** The stream carries it converted to Dolby Digital. */
  converted?: boolean;
}

/** How a film or an episode plays (D64, D68). */
export interface ItemLink extends PlayLink {
  /** player: the box seeks in the file; restart: the page asks for the stream from a time. */
  seek: 'player' | 'restart';
  /** Where to ask where the stream from a time starts, with ?t=, for restart. */
  start?: string;
  /** Seconds, 0 if not known. */
  duration: number;
  /** The stream's sound tracks, in its order, for restart. */
  audio?: StreamAudio[];
  /**
   * restart: the stream carries one sound track at a time, as the source's
   * server transcodes it (D71); another is picked by asking for the stream
   * again with ?a= and its place in audio.
   */
  audio_switch?: 'restart';
  /** Subtitle tracks the page may load (D69). */
  subtitles?: SubtitleInfo[];
}

/** How a past programme plays from its channel's archive (D81): always by asking for its stream from a time. */
export interface ArchiveLink extends ItemLink {
  programme: Programme;
}

/** A subtitle track that the page may load: SRT at url, with ?t= for a stream from a time. */
export interface SubtitleInfo {
  /** Such as eng; "" if not known. */
  language?: string;
  title?: string;
  forced?: boolean;
  /** For the deaf and hard of hearing. */
  hearing?: boolean;
  default?: boolean;
  url: string;
}

/** Where a stream that ffmpeg makes from a time starts, in seconds from the file's start. */
export interface StreamStart {
  t: number;
}

export interface Programme {
  id: number;
  title: string;
  /** Unix seconds. */
  start: number;
  stop: number;
  subtitle?: string;
  description?: string;
  category?: string;
}

export interface Guide {
  from: number;
  to: number;
  channels: { [id: string]: Programme[] };
}

export interface ChannelGuide {
  from: number;
  to: number;
  programmes: Programme[];
}

export interface SearchProgramme extends Programme {
  channel: number;
}

export interface SearchResult {
  channels: number[];
  programmes: SearchProgramme[];
  movies: TitleRow[];
  series: TitleRow[];
}

/** A kind of title: a film or a series. */
export type TitleKind = 'movie' | 'series';

/** A shelf of films or series: Continue Watching, Recently Added, or a source's category. */
export interface Shelf {
  /** continue, recent, or a category's ID. */
  id: string;
  name: string;
  /** -1 while not known: a portal's shelf until a part of it is read. */
  count: number;
}

/** Whether the box can browse films or series, and their shelves. */
export interface LibraryState {
  state: 'none' | 'loading' | 'ready' | 'failed';
  error?: string;
  shelves: Shelf[];
}

/** A film or a series on a shelf. */
export interface TitleRow {
  id: number;
  /** movie or series: a portal's shelf of films can hold series. */
  kind?: TitleKind;
  name: string;
  year?: number;
  /** Out of 100. */
  rating?: number;
  /** The path of its poster on Overscan; /artwork/thumb/ instead of /artwork/poster/ gives a small one. */
  poster?: string;
  /** Per cent watched, of a film, or on Continue Watching of a series' episode. */
  progress?: number;
  /** A film that the account finished. */
  watched?: boolean;
  /** On Continue Watching, a series' episode, such as S1 E3. */
  episode?: string;
  /** Playing it needs the parental PIN (D85). */
  locked?: boolean;
}

/** A part of a shelf. */
export interface ShelfPage {
  id: string;
  name: string;
  total: number;
  from: number;
  titles: TitleRow[];
  /** The from of the next part, -1 after the last; a part of a portal's shelf can hold fewer titles than asked for. */
  next?: number;
}

/** How far the account got into a film or an episode, in seconds. */
export interface Progress {
  position: number;
  duration: number;
  finished?: boolean;
  watched_at: number;
}

export interface EpisodeInfo {
  id: number;
  number: number;
  name?: string;
  /** Seconds. */
  duration?: number;
  plot?: string;
  picture?: string;
  progress?: Progress;
}

export interface Season {
  number: number;
  name: string;
  episodes: EpisodeInfo[];
}

/** A film or a series, opened. */
export interface Title {
  id: number;
  kind: TitleKind;
  name: string;
  year?: number;
  rating?: number;
  poster?: string;
  backdrop?: string;
  shelf?: string;
  plot?: string;
  genre?: string;
  cast?: string;
  director?: string;
  country?: string;
  released?: string;
  age_rating?: string;
  /** Minutes. */
  runtime?: number;
  /** False while its source's details have not been read. */
  details: boolean;
  details_error?: string;
  /** A film's. */
  progress?: Progress;
  /** A series'. */
  seasons?: Season[];
  /** A series' episode to play next, and where. */
  next?: { episode: number; position: number };
  /** Playing it, or a series' episodes, needs the parental PIN (D85). */
  locked?: boolean;
}

export interface Settings {
  home: 'simple' | 'picture';
  picture: string;
  accent: 'teal' | 'white' | 'blue' | 'orange';
  clock: '24' | '12';
  /** lineup, favourites (they count from 1) or groups (each counts from 1) (D56). */
  numbers: 'lineup' | 'favourites' | 'groups';
  /** preview: Live TV shows a group's channels as the focus rests on it; ok: only on OK or Right (D62). */
  groups: 'preview' | 'ok';
}

/** A clip of the playback check (D70). */
export interface CheckClip {
  id: string;
  /** What it tests, such as H.265 at 10 bits. */
  label: string;
  /** What to ask once it plays: whether the picture moves, the tone sounds, or both. */
  ask: 'picture' | 'sound' | 'both';
  url: string;
  solution: string;
  width: number;
  height: number;
}

/** Whether the box can be checked, and the clips once the server has made them. */
export interface CheckState {
  state: 'ready' | 'preparing' | 'unavailable';
  ready: number;
  total: number;
  /** When the box was last checked, in Unix seconds; 0 if never. */
  checked: number;
  clips?: CheckClip[];
}

/** What the page found of a clip of the check. */
export interface CheckResult {
  id: string;
  /** The viewer's answer: yes, no, or "" if not asked or not known. */
  answer: '' | 'yes' | 'no';
  /** The box said it started the clip, or could not open it. */
  started: boolean;
  failed: boolean;
  /** How long it took to start, in milliseconds. */
  ms: number;
  /** The picture's size as the box reported it, 0 if it did not. */
  width: number;
  height: number;
  /** How many sound tracks the box listed. */
  tracks: number;
  /** Where it got to, in seconds, when the viewer answered. */
  position: number;
  /** What the box's player said of the picture and the sound, as it said it. */
  video: string;
  audio: string;
  /** The player's events while the clip played, as the box gave them, such as 4 2 7. */
  events: string;
}

/**
 * PlaybackReport tells the server how a film or an episode started on the
 * box, or did not, for its log (POST playback).
 */
export interface PlaybackReport {
  kind: 'movie' | 'episode' | 'programme';
  /** The film's title ID, the episode's ID, or the ID of the channel whose past programme it is. */
  id: number;
  /** began: it started; not_started: it never did; waited: it stopped for a while and went on; stalled: it stopped and did not go on. */
  outcome: 'began' | 'not_started' | 'waited' | 'stalled';
  /** Milliseconds the server took to give the link. */
  link_ms: number;
  /** Milliseconds from the play command until it moved, -1 if it never did. */
  moved_ms: number;
  /** Milliseconds from the play command until the player said it started, -1 if it never did. */
  event_ms: number;
  /** Milliseconds it stood still, for waited and stalled. */
  wait_ms: number;
  /** The page paused it and played it again, since it stood still. */
  nudged: boolean;
  /** It came through ffmpeg. */
  converted: boolean;
  /** Where it started, in seconds. */
  from: number;
  /** The player's events since the play command, as the box gave them. */
  events: string;
}

/** What the server kept of a check. */
export interface CheckSaved {
  checked: number;
  findings: { label: string; plays: boolean }[];
}

const base = '/api/device/v1/';
const timeoutMs = 15000;

/** request sends one call and passes its answer, parsed, to done. */
function request<T>(method: string, path: string, token: string, body: object | null, done: Callback<T>): void {
  send<T>(method, base + path, token, body, done);
}

/** getFrom asks for JSON at an address on the server outside the API, such as a stream's start (D68). */
export function getFrom<T>(url: string, done: Callback<T>): void {
  send<T>('GET', url, '', null, done);
}

/** send makes one request and passes its answer, parsed, to done. */
function send<T>(method: string, url: string, token: string, body: object | null, done: Callback<T>): void {
  const xhr = new XMLHttpRequest();
  let finished = false;
  const finish = function (err: ApiError | null, data: T | null) {
    if (!finished) {
      finished = true;
      clearTimeout(timer);
      done(err, data);
    }
  };
  const timer = setTimeout(function () {
    finish({ status: 0, code: 'timeout', retryAfter: 0 }, null);
    xhr.abort();
  }, timeoutMs);
  xhr.onreadystatechange = function () {
    if (xhr.readyState !== 4) {
      return;
    }
    let data: any = null;
    try {
      data = xhr.responseText ? JSON.parse(xhr.responseText) : null;
    } catch (e) {
      data = null;
    }
    if (xhr.status === 0) {
      finish({ status: 0, code: 'network', retryAfter: 0 }, null);
    } else if (xhr.status >= 200 && xhr.status < 300 && data) {
      finish(null, data as T);
    } else {
      const code = data && typeof data.error === 'string' ? data.error : 'http_' + xhr.status;
      const retry = parseInt(xhr.getResponseHeader('Retry-After') || '', 10);
      finish({ status: xhr.status, code: code, retryAfter: retry > 0 ? retry : 0 }, null);
    }
  };
  xhr.open(method, url, true);
  if (token) {
    xhr.setRequestHeader('Authorization', 'Bearer ' + token);
  }
  if (body) {
    xhr.setRequestHeader('Content-Type', 'application/json');
    xhr.send(JSON.stringify(body));
  } else {
    xhr.send();
  }
}

/** Client keeps the box's session and makes calls with it. */
export class Client {
  private mac: string;
  private model: string;
  private secret: string;
  private keep: (secret: string) => void;
  private token = '';

  /**
   * The box sends secret, which pairs it with the server (D39), and keep
   * stores a new one that the server gives it.
   */
  constructor(mac: string, model: string, secret: string, keep: (secret: string) => void) {
    this.mac = mac;
    this.model = model;
    this.secret = secret;
    this.keep = keep;
  }

  /** start starts a session; an approved box gets a token for later calls. */
  start(done: Callback<Session>): void {
    const self = this;
    const body = { mac: this.mac, model: this.model, secret: this.secret };
    request<Session>('POST', 'session', '', body, function (err, s) {
      if (s && s.secret) {
        self.secret = s.secret;
        self.keep(s.secret);
      }
      if (s && s.state === 'approved' && s.token) {
        self.token = s.token;
      } else {
        self.token = '';
      }
      done(err, s);
    });
  }

  /**
   * call makes a call with the session. A call refused for want of a session
   * starts a new one and is made again once; if the box is no longer
   * approved, done gets the unauthorized error.
   */
  call<T>(method: string, path: string, body: object | null, done: Callback<T>): void {
    const self = this;
    request<T>(method, path, this.token, body, function (err, data) {
      if (!err || err.code !== 'unauthorized') {
        done(err, data);
        return;
      }
      self.start(function (serr, s) {
        if (!serr && s && s.state === 'approved') {
          request<T>(method, path, self.token, body, done);
        } else {
          done(err, null);
        }
      });
    });
  }
}

/** describe says what went wrong with a call, for the screen. */
export function describe(err: ApiError | null): string {
  if (!err) {
    return 'The server gave no answer.';
  }
  switch (err.code) {
    case 'network':
    case 'timeout':
      return 'The server did not answer.';
    case 'limit':
      return "All connections to this source are in use.";
    case 'busy':
      return 'The server is transcoding as many films as it may at once.';
    case 'no_archive':
      return "It isn't in the channel's archive.";
    case 'recording_off':
      return "The server doesn't record. Recording is turned on in the admin's settings.";
    case 'programme_ended':
      return 'It has ended.';
    case 'recorded':
      return 'It has been recorded already, and is on Movies.';
    case 'too_many':
      return 'There are as many recordings to come as there may be. Cancel some first.';
    case 'internal':
      return "The server failed. Its log says why.";
  }
  return 'The server answered ' + err.code + '.';
}
