// SPDX-License-Identifier: AGPL-3.0-or-later

// Settings: this box, who watches on it while the account has two profiles
// or more (D94), how the page looks on it, the parental controls, reloading
// the page, and what it is. Appearance is a page of its own.

import type { Settings } from '../api';
import { el, setHTML, setText, show } from '../dom';
import { arrows, icon } from '../icons';
import type { Press } from '../keys';
import { accentChoices, type Choice, clockChoices, cycle, groupsChoices, homeChoices, label, numbersChoices, pictureChoices, pictureURL } from '../settings';
import { clock, date, now } from '../time';
import { Clock, type Ctx, escape, type Screen, type VideoArea } from './screen';

interface Row {
  icon: string;
  label: string;
  value: () => string;
  /** Room above the row. */
  gap?: boolean;
  info: () => Info;
  ok?: () => void;
  /** Changes the row's value, for a row whose value Left and Right change. */
  change?: (delta: number) => void;
  /** Whether the row shows; always, if missing. */
  when?: () => boolean;
}

interface Info {
  icon: string;
  title: string;
  text: string;
  facts: [string, string][];
}

interface Option {
  name: keyof Settings;
  label: string;
  choices: Choice[];
  /** Only for Home with a picture. */
  pictureOnly?: boolean;
}

const options: Option[] = [
  { name: 'home', label: 'Home Screen', choices: homeChoices },
  { name: 'picture', label: 'Home Picture', choices: pictureChoices, pictureOnly: true },
  { name: 'accent', label: 'Accent Colour', choices: accentChoices },
  { name: 'clock', label: 'Clock', choices: clockChoices },
];

export class SettingsScreen implements Screen {
  el: HTMLElement;
  tunes = false;
  private ctx: Ctx;
  private appearance = false;
  private focus = 0;
  /** Coming back from Playback Check, the focus stays where it was. */
  private keepFocus = false;
  private focusOption = 0;
  private rows: Row[];

  private back: HTMLElement;
  private title: HTMLElement;
  private infoIcon: HTMLElement;
  private infoTitle: HTMLElement;
  private infoText: HTMLElement;
  private infoFacts: HTMLElement;
  private preview: HTMLElement;
  private list: HTMLElement;
  private note: HTMLElement;
  private clockView: Clock;

  constructor(parent: HTMLElement, ctx: Ctx) {
    this.ctx = ctx;
    this.el = el('div', 'screen settings', parent);
    const head = el('div', 'page-head', this.el);
    this.back = el('span', 'back', head);
    this.title = el('span', 'page-title', head);
    this.clockView = new Clock(head, false);

    const info = el('div', 'settings-info', this.el);
    this.infoIcon = el('div', 'info-icon', info);
    this.infoTitle = el('div', 'info-title', info);
    this.infoText = el('div', 'info-text', info);
    this.infoFacts = el('div', 'info-facts', info);
    this.preview = el('div', 'preview', info);
    this.list = el('div', 'settings-list', this.el);
    this.note = el('div', 'settings-note', this.el);

    const self = this;
    this.rows = [
      {
        icon: 'box',
        label: 'This Box',
        value: () => [ctx.server.label, ctx.platform.model].filter(Boolean).join(' · '),
        info: () => self.boxInfo(),
      },
      {
        icon: 'profile',
        label: 'Profile',
        value: () => ctx.profiles.name(),
        when: () => ctx.profiles.several(),
        info: () => ({
          icon: 'profile',
          title: 'Profile',
          text:
            'Who is watching on this box. Each profile keeps its own favourite channels, and where its films and episodes stopped; this box’s appearance and the channels it played last stay the same whoever watches. OK, or the yellow key on Home, switches to another profile. The profiles are set in the admin, on the Devices page.',
          facts: [
            ['Watching', ctx.profiles.name()],
            ['Profiles', String(ctx.profiles.list.length)],
          ],
        }),
        ok: () => ctx.switchProfile(),
      },
      {
        icon: 'appearance',
        label: 'Appearance',
        value: () => {
          const s = ctx.settings.current;
          return label(homeChoices, s.home) + ' Home · ' + label(accentChoices, s.accent) + ' · ' + label(clockChoices, s.clock) + ' Clock';
        },
        info: () => ({
          icon: 'appearance',
          title: 'Appearance',
          text: 'How Home looks, the accent colour and the clock. Each box keeps its own.',
          facts: [],
        }),
        ok: () => self.openAppearance(),
      },
      {
        icon: 'guide',
        label: 'Channel Numbers',
        value: () => label(numbersChoices, ctx.settings.current.numbers),
        info: () => ({
          icon: 'guide',
          title: 'Channel Numbers',
          text:
            'How the lists number their channels on this box. As in the Lineup shows the numbers the server gives. Favourites from 1 numbers your favourites 1, 2, 3 in their order, and Each Group from 1 does so in every group; All Channels keeps the lineup’s numbers. A number you type picks the channel of that number in the list you are in.',
          facts: [],
        }),
        change: (delta: number) => {
          ctx.settings.set('numbers', cycle(numbersChoices, ctx.settings.current.numbers, delta));
        },
      },
      {
        icon: 'tv',
        label: 'Groups and Shelves',
        value: () => label(groupsChoices, ctx.settings.current.groups),
        info: () => ({
          icon: 'tv',
          title: 'Groups and Shelves',
          text:
            'What Live TV does as you move through its groups, and Movies and Series through their shelves. Preview as You Move shows the channels or titles of the one you stop on. Open with OK leaves them as they are until you press OK or Right, so the list keeps up with the remote on a slow box.',
          facts: [],
        }),
        change: (delta: number) => {
          ctx.settings.set('groups', cycle(groupsChoices, ctx.settings.current.groups, delta));
        },
      },
      {
        icon: 'parental',
        label: 'Parental Controls',
        value: () => {
          const n = self.lockedCount();
          return n ? n + (n === 1 ? ' locked channel' : ' locked channels') : 'Off';
        },
        info: () => ({
          icon: 'parental',
          title: 'Parental Controls',
          text:
            'Locked channels ask for the PIN before they play, and the box keeps a right PIN for 10 minutes. The PIN and the locks are set on the server, on the Lineup page of the admin. OK makes this box ask again now.',
          facts: [['Locked channels', String(self.lockedCount())]],
        }),
        ok: () => {
          ctx.player.forgetPIN();
          ctx.toast('This box will ask for the PIN again.');
        },
      },
      {
        icon: 'playcheck',
        label: 'Playback Check',
        value: () => {
          const at = ctx.check.checked;
          return at > 0 ? 'Checked ' + date(at) : at === 0 ? 'Not checked yet' : '';
        },
        info: () => ({
          icon: 'playcheck',
          title: 'Playback Check',
          text:
            'Plays short clips to learn which kinds of film this box plays, so that the server converts only what the box can’t play. It takes about four minutes, with the sound up.',
          facts: [
            ['Last checked', ctx.check.checked > 0 ? date(ctx.check.checked) : ctx.check.checked === 0 ? 'Never' : ''],
            ['Firmware', ctx.platform.firmware || 'Not known'],
          ],
        }),
        ok: () => {
          self.keepFocus = true;
          ctx.go('check');
        },
      },
      {
        icon: 'reload',
        label: 'Reload the Portal',
        value: () => 'Get the newest interface',
        gap: true,
        info: () => ({
          icon: 'reload',
          title: 'Reload the Portal',
          text: 'Loads this page again from the server, with its newest version and the lineup.',
          facts: [],
        }),
        ok: () => {
          ctx.player.stop();
          location.reload();
        },
      },
      {
        icon: 'about',
        label: 'About',
        value: () => 'Overscan ' + ctx.server.version,
        info: () => ({
          icon: 'about',
          title: 'Overscan',
          text: 'A free, open-source portal for the channels of a household, under the GNU Affero General Public License.',
          facts: [
            ['Server', ctx.server.version],
            ['Address', location.host],
          ],
        }),
      },
    ];
  }

  private lockedCount(): number {
    let n = 0;
    const list = this.ctx.lineup.channels;
    for (let i = 0; i < list.length; i++) {
      if (list[i]!.locked) {
        n++;
      }
    }
    return n;
  }

  private boxInfo(): Info {
    const p = this.ctx.platform;
    const w = window.innerWidth || 1280;
    const h = window.innerHeight || 720;
    const facts: [string, string][] = [
      ['Name', this.ctx.server.label || 'No name yet'],
      ['Model', p.model + ' · ' + p.mac],
      ['Paired', 'Yes'],
      ['Server', 'Connected, ' + location.host],
      ['Picture', w + '×' + h + ' page' + (p.outputMode ? ', ' + p.outputMode.replace(/-\d+$/, '') + ' output' : '')],
    ];
    return { icon: 'box', title: 'This Box', text: 'How this box is known to the server, and what it shows.', facts: facts };
  }

  enter(): void {
    this.appearance = false;
    if (!this.keepFocus) {
      this.focus = 0;
    }
    this.keepFocus = false;
    this.ctx.check.load();
    this.draw();
  }

  /** checkChanged redraws the rows once the server has said when the box was last checked. */
  checkChanged(): void {
    if (!this.appearance) {
      this.draw();
    }
  }

  leave(): void {}

  private openAppearance(): void {
    this.appearance = true;
    this.focusOption = 0;
    this.draw();
  }

  /** shows says whether the row at i shows. */
  private shows(i: number): boolean {
    const when = this.rows[i]!.when;
    return !when || when();
  }

  /** step moves the focus to the next row that shows, by delta, wrapping around. */
  private step(delta: number): void {
    const n = this.rows.length;
    for (let k = 1; k <= n; k++) {
      const i = (((this.focus + delta * k) % n) + n) % n;
      if (this.shows(i)) {
        this.focus = i;
        return;
      }
    }
  }

  /** rowAt returns the place of the row with the label. */
  private rowAt(label: string): number {
    for (let i = 0; i < this.rows.length; i++) {
      if (this.rows[i]!.label === label) {
        return i;
      }
    }
    return 0;
  }

  /** profilesChanged shows or hides the Profile row, and redraws its value. */
  profilesChanged(): void {
    if (!this.appearance) {
      this.draw();
    }
  }

  private draw(): void {
    setText(this.back, this.appearance ? '‹ Settings' : '‹ Home');
    setText(this.title, this.appearance ? 'Appearance' : 'Settings');
    let html = '';
    if (!this.appearance) {
      if (!this.shows(this.focus)) {
        this.step(1);
      }
      for (let i = 0; i < this.rows.length; i++) {
        const r = this.rows[i]!;
        if (!this.shows(i)) {
          continue;
        }
        const value = r.change
          ? '<span class="arrow">' + arrows('l') + '</span><span class="choice">' + escape(r.value()) + '</span><span class="arrow">' + arrows('r') + '</span>'
          : '<span class="value">' + escape(r.value()) + '</span><span class="chev">›</span>';
        html +=
          '<div class="setting' +
          (i === this.focus ? ' focus' : '') +
          (r.gap ? ' gap' : '') +
          '">' +
          icon(r.icon, 17) +
          '<span class="label">' +
          escape(r.label) +
          '</span>' +
          value +
          '</div>';
      }
      setHTML(this.list, html);
      setText(this.note, '');
      this.drawInfo(this.rows[this.focus]!.info());
      show(this.preview, false);
      return;
    }
    const s = this.ctx.settings.current;
    for (let i = 0; i < options.length; i++) {
      const o = options[i]!;
      const off = o.pictureOnly && s.home !== 'picture';
      const value = (s as any)[o.name] as string;
      let shown = escape(label(o.choices, value));
      if (o.name === 'accent') {
        let dots = '';
        for (let k = 0; k < accentChoices.length; k++) {
          const a = accentChoices[k]!.value;
          dots += '<span class="dot accent-' + a + (a === value ? ' on' : '') + '"></span>';
        }
        shown = '<span class="dots">' + dots + '</span><span class="name">' + shown + '</span>';
      }
      html +=
        '<div class="setting option' +
        (i === this.focusOption ? ' focus' : '') +
        (off ? ' off' : '') +
        '"><span class="label">' +
        escape(o.label) +
        '</span>' +
        (off
          ? '<span class="value">With a Picture only</span>'
          : '<span class="arrow">' + arrows('l') + '</span><span class="choice">' + shown + '</span><span class="arrow">' + arrows('r') + '</span>') +
        '</div>';
    }
    setHTML(this.list, html);
    setText(this.note, 'Each box keeps its own appearance. Left and right change the row in focus.');
    this.drawPreview();
  }

  private drawInfo(info: Info): void {
    setHTML(this.infoIcon, icon(info.icon, 68, 1.4));
    setText(this.infoTitle, info.title);
    setText(this.infoText, info.text);
    let html = '';
    for (let i = 0; i < info.facts.length; i++) {
      html += '<div class="fact"><span>' + escape(info.facts[i]![0]) + '</span><span class="v">' + escape(info.facts[i]![1]) + '</span></div>';
    }
    setHTML(this.infoFacts, html);
    show(this.infoFacts, true);
  }

  private drawPreview(): void {
    const s = this.ctx.settings.current;
    const o = options[this.focusOption]!;
    show(this.preview, true);
    show(this.infoFacts, false);
    setHTML(this.infoIcon, icon('appearance', 64, 1.4));
    if (o.name === 'home' || o.name === 'picture') {
      setText(this.infoTitle, 'Home Screen');
      setText(this.infoText, 'Simple shows six big buttons on a plain background. With a Picture adds a picture, your last channel and your favourite channels.');
      setHTML(
        this.preview,
        '<div class="thumb' +
          (s.home === 'simple' ? ' on' : '') +
          '"><div class="mini-grid"><span class="first"></span><span></span><span></span><span></span><span></span><span></span></div><div class="cap">Simple</div></div>' +
          '<div class="thumb' +
          (s.home === 'picture' ? ' on' : '') +
          '"><div class="mini-picture" style="background-image:url(&quot;' +
          pictureURL(s.picture) +
          '&quot;)"><span class="mini-row"><span class="first"></span><span></span><span></span><span></span><span></span><span></span></span></div><div class="cap">With a Picture</div></div>',
      );
    } else if (o.name === 'accent') {
      setText(this.infoTitle, 'Accent Colour');
      setText(this.infoText, 'The colour of progress bars and of the guide’s line for now.');
      setHTML(this.preview, '<div class="accent-sample"><span class="bar"><span class="fill acc"></span></span><span class="chip acc">' + escape(clock(now())) + '</span></div>');
    } else {
      setText(this.infoTitle, 'Clock');
      setText(this.infoText, 'Times on a 24-hour clock, such as 20:41, or a 12-hour one, such as 8:41 pm.');
      setHTML(this.preview, '<div class="clock-sample">' + escape(clock(now())) + '</div>');
    }
  }

  key(p: Press): boolean {
    if (!this.appearance) {
      switch (p.key) {
        case 'up':
          this.step(-1);
          break;
        case 'down':
          this.step(1);
          break;
        case 'left':
        case 'right':
        case 'ok': {
          const r = this.rows[this.focus]!;
          if (r.change) {
            r.change(p.key === 'left' ? -1 : 1);
          } else if (r.ok && (p.key === 'ok' || (p.key === 'right' && r.label === 'Appearance'))) {
            r.ok();
          } else if (p.key === 'left') {
            return false;
          }
          break;
        }
        default:
          return false;
      }
      this.draw();
      return true;
    }
    const s = this.ctx.settings.current;
    const o = options[this.focusOption]!;
    const off = o.pictureOnly && s.home !== 'picture';
    switch (p.key) {
      case 'up':
        this.focusOption = (this.focusOption + options.length - 1) % options.length;
        break;
      case 'down':
        this.focusOption = (this.focusOption + 1) % options.length;
        break;
      case 'left':
      case 'right':
      case 'ok':
        if (!off) {
          const value = (s as any)[o.name] as string;
          this.ctx.settings.set(o.name, cycle(o.choices, value, p.key === 'left' ? -1 : 1));
        }
        break;
      case 'back':
        this.appearance = false;
        this.focus = this.rowAt('Appearance');
        break;
      default:
        return false;
    }
    this.draw();
    return true;
  }

  tick(): void {
    this.clockView.tick();
  }

  lineupChanged(): void {
    this.draw();
  }

  playback(): void {}

  video(): VideoArea {
    return 'none';
  }
}

