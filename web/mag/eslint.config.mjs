// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Lints the built page, not its sources: parsed as ES5, the bundle must not
// use what TypeScript's es5 and dom libraries declare but the box lacks
// (D32). It runs after the build.

const missing = 'the MAG322w1 has no such thing (D32)';

export default [
  {
    files: ['dist/*.js'],
    languageOptions: {
      ecmaVersion: 5,
      sourceType: 'script',
    },
    rules: {
      'no-restricted-globals': [
        'error',
        { name: 'fetch', message: `${missing}; use XMLHttpRequest` },
        { name: 'URLSearchParams', message: `${missing}; build query strings by hand` },
        { name: 'URL', message: 'the box has a URL constructor that does not parse addresses' },
        { name: 'TextDecoder', message: missing },
        { name: 'Intl', message: `${missing}; format dates and numbers by hand` },
        { name: 'localStorage', message: `${missing}; see D38` },
      ],
      'no-restricted-properties': [
        'error',
        { property: 'localStorage', message: `${missing}; see D38` },
        { property: 'fetch', message: `${missing}; use XMLHttpRequest` },
      ],
      'no-restricted-syntax': [
        'error',
        {
          selector: "AssignmentExpression[left.property.name='responseType'][right.value='json']",
          message: "the box's XMLHttpRequest has no json response type; parse responseText",
        },
      ],
    },
  },
];
