// SPDX-License-Identifier: AGPL-3.0-or-later

// The profiles of the box's account (D94): who watches, each with their own
// favourite channels and resume points. The box plays as one of them, and
// switches to another from Home or Settings. While the account has one
// profile, the page shows none of this.

import type { Client, Profile, Profiles } from './api';

export class ProfilesModel {
  /** The profile the box plays as; 0 until the server says. */
  current = 0;
  /** The account's profiles, the first first. */
  list: Profile[] = [];
  /** The stylesheet of the theme the box shows, or "" for Overscan's own (D97). */
  theme = '';
  private client: Client;

  constructor(client: Client) {
    this.client = client;
  }

  /** load asks the server for the account's profiles, and calls done whether or not it could. */
  load(done: () => void): void {
    const self = this;
    this.client.call<Profiles>('GET', 'profiles', null, function (err, p) {
      if (!err && p) {
        self.set(p);
      }
      done();
    });
  }

  /** set keeps what the server said of the profiles. */
  set(p: Profiles): void {
    this.current = p.profile;
    this.list = p.profiles || [];
    this.theme = p.theme || '';
  }

  /** several says whether the account has two profiles or more, so that the page shows them. */
  several(): boolean {
    return this.list.length > 1;
  }

  /** name returns the name of the profile the box plays as, or "". */
  name(): string {
    for (let i = 0; i < this.list.length; i++) {
      if (this.list[i]!.id === this.current) {
        return this.list[i]!.name;
      }
    }
    return '';
  }
}
