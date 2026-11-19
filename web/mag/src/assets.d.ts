// SPDX-License-Identifier: AGPL-3.0-or-later

// The names that the build gives the files in src/assets, which carry a
// hash of their content: {"pictures/dusk.jpg": "dusk.0123456789.jpg"}.
declare module 'assets' {
  const names: { [file: string]: string };
  export default names;
}
