// SPDX-License-Identifier: AGPL-3.0-or-later

// The page's icons, drawn as strokes on a 24-unit grid, in the text's
// colour. Each SVG sits in a span of a set size: the box's browser gives an
// SVG that is itself a flex item no width, and does not size one by rem in
// its attributes everywhere.

const paths: { [name: string]: string } = {
  tv: '<path d="M4 7h16a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 20 19H4a1.5 1.5 0 0 1-1.5-1.5v-9A1.5 1.5 0 0 1 4 7z"/><path d="M8.5 3.5L12 7l3.5-3.5"/>',
  guide:
    '<path d="M4 4.5h16a1 1 0 0 1 1 1v13a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1v-13a1 1 0 0 1 1-1z"/><path d="M3 9.5h18"/><path d="M9 9.5v10M13 13.5h5M13 16.5h3"/>',
  movies:
    '<path d="M4.5 3.5h15a1 1 0 0 1 1 1v15a1 1 0 0 1-1 1h-15a1 1 0 0 1-1-1v-15a1 1 0 0 1 1-1z"/><path d="M7.5 3.5v17M16.5 3.5v17"/><path d="M3.5 8h4M3.5 12h17M3.5 16h4M16.5 8h4M16.5 16h4"/>',
  series: '<path d="M4 8h16a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1z"/><path d="M6 5h12M8 2.5h8"/><path d="M10 11.5v5l4-2.5z"/>',
  search: '<path d="M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14z"/><path d="M20.5 20.5l-4.6-4.6"/>',
  settings:
    '<path d="M12 8.8a3.2 3.2 0 1 0 0 6.4 3.2 3.2 0 0 0 0-6.4z"/><path d="M12 2.8v2.6M12 18.6v2.6M2.8 12h2.6M18.6 12h2.6"/><path d="M5.5 5.5l1.85 1.85M16.65 16.65l1.85 1.85M5.5 18.5l1.85-1.85M16.65 7.35l1.85-1.85"/>',
  lock: '<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  box: '<rect x="3" y="9" width="18" height="8" rx="2"/><path d="M7 13h.01M10 13h.01"/><path d="M15.5 13h2.5"/><path d="M8 5.5c2.3-1.6 5.7-1.6 8 0"/>',
  appearance: '<circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 0 0 18z" fill="currentColor"/>',
  parental: '<path d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6z"/>',
  reload: '<path d="M20 12a8 8 0 1 1-2.3-5.6M20 4v5h-5"/>',
  about: '<path d="M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8h.01"/>',
  server: '<rect x="4" y="4" width="16" height="7" rx="1.5"/><rect x="4" y="13" width="16" height="7" rx="1.5"/><path d="M8 7.5h.01M8 16.5h.01"/>',
  play: '<path d="M7 4.5v15l12.5-7.5z" fill="currentColor"/>',
  pause: '<path d="M8 5v14M16 5v14"/>',
  left: '<path d="M15 5l-7 7 7 7"/>',
  check: '<path d="M5 12.5l4.5 4.5L19 7.5"/>',
  playcheck:
    '<path d="M4 4.5h16a1.5 1.5 0 0 1 1.5 1.5v10a1.5 1.5 0 0 1-1.5 1.5H4A1.5 1.5 0 0 1 2.5 16V6A1.5 1.5 0 0 1 4 4.5z"/><path d="M8.5 21h7"/><path d="M8.5 11l2.5 2.5 4.5-5"/>',
  right: '<path d="M9 5l7 7-7 7"/>',
  radio: '<rect x="3" y="9" width="18" height="11" rx="2"/><path d="M7 9l9.5-5"/><circle cx="8.5" cy="14.5" r="2.5"/><path d="M14 12.5h3.5M14 16.5h3.5"/>',
  sound: '<path d="M4 9.5h3.5L12 6v12l-4.5-3.5H4z"/><path d="M15.5 9.5a3.5 3.5 0 0 1 0 5M18 7a7 7 0 0 1 0 10"/>',
};

/** box returns an SVG's markup in a span size pixels square, in rem. */
function box(className: string, size: number, svg: string): string {
  const rem = size / 16 + 'rem';
  return '<span class="' + className + '" style="width:' + rem + ';height:' + rem + '">' + svg + '</span>';
}

/** icon returns the markup of an icon, size pixels square. */
export function icon(name: string, size: number, strokeWidth?: number): string {
  return box(
    'ico',
    size,
    '<svg width="100%" height="100%" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="' +
      (strokeWidth || 1.8) +
      '" stroke-linecap="round" stroke-linejoin="round">' +
      (paths[name] || '') +
      '</svg>',
  );
}

/** star returns the markup of a filled star, for a favourite. */
export function star(size: number): string {
  return box(
    'ico star',
    size,
    '<svg width="100%" height="100%" viewBox="0 0 24 24" stroke-width="1.5" stroke-linejoin="round">' +
      '<path d="M12 3.2l2.7 5.5 6 .9-4.35 4.25 1 6L12 17l-5.35 2.85 1-6L3.3 9.6l6-.9z"/></svg>',
  );
}

/** arrows returns the markup of small triangles pointing the given ways, for a key hint. */
export function arrows(ways: string): string {
  const shapes: { [w: string]: string } = {
    l: 'M8 2L2 6l6 4z',
    r: 'M4 2l6 4-6 4z',
    u: 'M2 8l4-6 4 6z',
    d: 'M2 4l4 6 4-6z',
  };
  let out = '';
  for (let i = 0; i < ways.length; i++) {
    out += box('ico tri', 8, '<svg width="100%" height="100%" viewBox="0 0 12 12"><path d="' + shapes[ways.charAt(i)] + '"/></svg>');
  }
  return out;
}
