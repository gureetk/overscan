// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Builds the MAG page from src/ into dist/, which is committed and embedded
// in the server (D32):
//
// - the TypeScript sources, compiled to ES5 by Babel one file at a time and
//   bundled by Rollup into one script, minified for ES5 and parsed as ES5
//   to make sure;
// - the stylesheet, with the -webkit- prefixes that the box needs;
// - the files in src/assets: fonts, pictures and the like, which the
//   stylesheet names as url("asset:fonts/x.woff") and the script imports as
//   the virtual module "assets";
// - index.html, naming the script and stylesheet.
//
// Every file but index.html carries a hash of its content in its name, so a
// box never uses a stale copy from its cache, and the server never lets a
// box cache index.html. The same sources always give the same files.

import { transformAsync } from '@babel/core';
import terser from '@rollup/plugin-terser';
import { parse } from 'acorn';
import autoprefixer from 'autoprefixer';
import { createHash } from 'node:crypto';
import { copyFile, mkdir, readdir, readFile, rm, writeFile } from 'node:fs/promises';
import { dirname, extname, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import postcss from 'postcss';
import { rollup } from 'rollup';

const root = dirname(fileURLToPath(import.meta.url));
const src = join(root, 'src');
const dist = join(root, 'dist');
const assetsDir = join(src, 'assets');

// The box's browser matches Safari 7.
const target = 'safari 7';

const banner = '/*! Overscan MAG page. SPDX-License-Identifier: AGPL-3.0-or-later */';

const babelOptions = {
  babelrc: false,
  configFile: false,
  sourceMaps: false,
  // The sources iterate over arrays only and rely on none of what these
  // assumptions give up, which keeps out helpers that need newer built-ins.
  assumptions: {
    constantSuper: true,
    ignoreFunctionLength: true,
    ignoreToPrimitiveHint: true,
    iterableIsArray: true,
    noClassCalls: true,
    noDocumentAll: true,
    noNewArrows: true,
    objectRestNoSymbols: true,
    privateFieldsAsProperties: true,
    setClassMethods: true,
    setComputedProperties: true,
    setPublicClassFields: true,
    setSpreadProperties: true,
    skipForOfIteratorClosing: true,
    superIsCallableConstructor: true,
  },
  // Presets run last to first: TypeScript is stripped, then preset-env
  // compiles what is left for the box.
  presets: [
    ['@babel/preset-env', { targets: target, modules: false }],
    ['@babel/preset-typescript', {}],
  ],
};

// assetFiles lists the files under src/assets, as paths relative to it with
// forward slashes, in a fixed order.
async function assetFiles(dir = assetsDir) {
  const out = [];
  for (const entry of (await readdir(dir, { withFileTypes: true })).sort((a, b) => (a.name < b.name ? -1 : 1))) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      out.push(...(await assetFiles(path)));
    } else {
      out.push(relative(assetsDir, path).split(sep).join('/'));
    }
  }
  return out;
}

// copyAssets copies the files under src/assets to dist with hashed names,
// apart from licence texts, and returns the names: {"fonts/x.woff":
// "x.0123456789.woff"}.
async function copyAssets() {
  const names = {};
  for (const file of await assetFiles()) {
    const from = join(assetsDir, ...file.split('/'));
    const ext = extname(file);
    const base = file.split('/').pop().slice(0, -ext.length);
    if (ext === '.txt') {
      // A licence that goes with the files beside it, such as the fonts'.
      names[file] = file.split('/').join('-');
    } else {
      names[file] = hashed(base, ext.slice(1), await readFile(from));
    }
    await copyFile(from, join(dist, names[file]));
  }
  return names;
}

// virtualAssets gives the script the asset names as the module "assets".
function virtualAssets(names) {
  return {
    name: 'assets',
    resolveId(id) {
      return id === 'assets' ? '\0assets' : null;
    },
    load(id) {
      return id === '\0assets' ? `export default ${JSON.stringify(names)};` : null;
    },
  };
}

// typescript resolves relative imports to .ts files and compiles each with
// Babel as Rollup loads it.
const typescript = {
  name: 'typescript',
  resolveId(id, importer) {
    if (!importer || !id.startsWith('.')) {
      return null;
    }
    return resolve(dirname(importer), id.endsWith('.ts') ? id : id + '.ts');
  },
  async transform(code, id) {
    if (!id.endsWith('.ts')) {
      return null;
    }
    const out = await transformAsync(code, { ...babelOptions, filename: id });
    return { code: out.code, map: null };
  },
};

async function buildScript(names) {
  const bundle = await rollup({
    input: join(src, 'main.ts'),
    plugins: [virtualAssets(names), typescript],
    onwarn(warning) {
      throw new Error(`rollup: ${warning.message}`);
    },
  });
  const { output } = await bundle.generate({
    format: 'iife',
    generatedCode: 'es5',
    banner,
    plugins: [terser({ ecma: 5, format: { comments: /^!/ } })],
  });
  await bundle.close();
  const code = output[0].code;
  parse(code, { ecmaVersion: 5 }); // throws on anything newer than ES5
  return code;
}

async function buildStyle(names) {
  const from = join(src, 'style.css');
  const css = (await readFile(from, 'utf8')).replace(/url\("asset:([^"]+)"\)/g, function (_, file) {
    if (!names[file]) {
      throw new Error(`style.css names ${file}, which is not in src/assets`);
    }
    return `url("${names[file]}")`;
  });
  const result = await postcss([autoprefixer({ overrideBrowserslist: [target] })]).process(css, {
    from,
    to: join(dist, 'style.css'),
    map: false,
  });
  for (const warning of result.warnings()) {
    throw new Error(`postcss: ${warning.toString()}`);
  }
  return result.css;
}

// hashed returns a file name with a hash of the content: app.0123456789.js.
function hashed(name, ext, content) {
  const hash = createHash('sha256').update(content).digest('hex').slice(0, 10);
  return `${name}.${hash}.${ext}`;
}

// replaceOnce replaces the only occurrence of what in s.
function replaceOnce(s, what, by) {
  if (s.split(what).length !== 2) {
    throw new Error(`index.html must name ${what} once`);
  }
  return s.replace(what, by);
}

await rm(dist, { recursive: true, force: true });
await mkdir(dist);
const names = await copyAssets();
const script = await buildScript(names);
const style = await buildStyle(names);
const scriptName = hashed('app', 'js', script);
const styleName = hashed('app', 'css', style);
let html = await readFile(join(src, 'index.html'), 'utf8');
html = replaceOnce(html, 'href="app.css"', `href="${styleName}"`);
html = replaceOnce(html, 'src="app.js"', `src="${scriptName}"`);

await writeFile(join(dist, scriptName), script);
await writeFile(join(dist, styleName), style);
await writeFile(join(dist, 'index.html'), html);
console.log(
  `dist: index.html, ${scriptName} (${script.length} bytes), ${styleName} (${style.length} bytes), ` +
    `${Object.keys(names).length} assets`,
);
