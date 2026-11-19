// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Overscan probe. It runs in a set-top box's browser and reports what that
// browser supports, so that the MAG UI's toolchain and polyfills can be chosen
// from facts (docs/plan.md, section 9, and docs/probe.md).
//
// It is hand-written ES5 because it has to run before anyone knows what the
// browser can do, and CI parses it as ES5. Every test is guarded, so one
// failure cannot stop the report.
//
// The MAG objects and gSTB methods it touches are documented in Infomir's
// public JavaScript API reference: https://wiki.infomir.eu/pub/doc/apinew/
// It does not call the MAC address or serial number getters.
//
// Version 2 also tests which storage survives a reboot: each run reads the
// markers the run before left, then leaves new ones (docs/probe.md).

(function () {
  'use strict';

  var PROBE_VERSION = 2;

  // Objects the JS API reference documents on MAG boxes.
  var MAG_OBJECTS = [
    'gSTB', 'stbStorage', 'stbWebWindow', 'stbWildWebWindow', 'stbWindowMgr', 'stbEvent',
    'stbUpdate', 'stbDownloadManager', 'pvrManager', 'dvbManager', 'timeShift'
  ];

  // gSTB getters that describe the device and its firmware. No identifiers.
  var GSTB_GETTERS = [
    'GetDeviceModel', 'GetDeviceModelExt', 'GetDeviceVendor', 'GetDeviceVersionHardware',
    'GetDeviceImageDesc', 'GetDeviceImageVersion', 'GetDeviceImageVersionCurrent',
    'GetDeviceActiveBank', 'Version'
  ];

  // Boot loader variables for gSTB.GetEnv: tvsystem is the video output
  // mode (for example 1080p-60), graphicres the graphic resolution.
  var GSTB_ENV = '{"varList":["tvsystem","graphicres"]}';

  // The name under which each run leaves its persistence markers.
  var MARK = 'overscan-probe-mark';
  var MARK_FILE = 'overscan-probe-mark.txt';
  var COOKIE_YEARS = 10;

  var ES5_BUILTINS = [
    'Function.prototype.bind', 'Array.isArray', 'Array.prototype.forEach',
    'Array.prototype.map', 'Array.prototype.filter', 'Array.prototype.reduce',
    'Array.prototype.indexOf', 'Array.prototype.some', 'Array.prototype.every',
    'Object.keys', 'Object.create', 'Object.defineProperty', 'Object.getPrototypeOf',
    'Object.getOwnPropertyNames', 'Object.freeze', 'String.prototype.trim', 'Date.now',
    'Date.prototype.toISOString', 'JSON.parse', 'JSON.stringify'
  ];

  var NEWER_BUILTINS = [
    'Promise', 'Map', 'Set', 'WeakMap', 'Symbol', 'Proxy', 'Reflect', 'Array.from',
    'Array.prototype.find', 'Array.prototype.includes', 'Object.assign',
    'String.prototype.startsWith', 'String.prototype.includes', 'Number.isNaN',
    'ArrayBuffer', 'Uint8Array', 'Intl', 'fetch', 'URL', 'URLSearchParams', 'TextDecoder',
    'requestAnimationFrame', 'webkitRequestAnimationFrame', 'performance.now', 'matchMedia',
    'history.pushState', 'WebSocket', 'EventSource', 'Blob', 'FormData', 'Worker',
    'MutationObserver', 'WebKitMutationObserver', 'CustomEvent'
  ];

  // Syntax newer than ES5. It is compiled with new Function, so this file
  // itself stays ES5.
  var NEWER_SYNTAX = {
    let_const: 'let a = 1; const b = 2;',
    arrow_functions: 'var f = (a) => a;',
    classes: 'class A {}',
    template_literals: 'var s = `a`;',
    destructuring: 'var o = {a: 1}; var {a} = o;',
    default_parameters: 'function f(a = 1) { return a; }',
    spread: 'var a = [...[1]];',
    for_of: 'for (var x of []) {}',
    async_functions: 'async function f() {}'
  };

  // 1x1 images, one per format.
  var IMAGES = {
    png: 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR42mOwvXkQAAMuAdhxHiViAAAAAElFTkSuQmCC',
    jpeg: 'data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAA0JCgsKCA0LCgsODg0PEyAVExISEyccHhcgLikxMC4pLSwzOko+MzZGNywtQFdBRkxOUlNSMj5aYVpQYEpRUk//2wBDAQ4ODhMREyYVFSZPNS01T09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT09PT0//wAARCAABAAEDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwDZoooroPlD/9k=',
    gif: 'data:image/gif;base64,R0lGODdhAQABAIEAAD3ZwQAAAAAAAAAAACwAAAAAAQABAAAIBAABBAQAOw==',
    webp_lossy: 'data:image/webp;base64,UklGRjQAAABXRUJQVlA4ICgAAABwAQCdASoBAAEAAsBMJaACdAFAAAD+6irq0mkXn/5sD/28/8Ee0oAA',
    webp_lossless: 'data:image/webp;base64,UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ7PY0uP+BiOh/AAA=',
    svg: 'data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHdpZHRoPSIxIiBoZWlnaHQ9IjEiPjxyZWN0IHdpZHRoPSIxIiBoZWlnaHQ9IjEiIGZpbGw9IiMzZGQ5YzEiLz48L3N2Zz4='
  };

  // How long one image may take before it counts as failed.
  var IMAGE_TIMEOUT_MS = 3000;

  var report = { probe_version: PROBE_VERSION, errors: [] };

  window.onerror = function (message, source, line) {
    report.errors.push('uncaught: ' + message + ' at line ' + line);
  };

  function describe(e) {
    return String((e && e.message) || e);
  }

  // guard runs fn and records an exception instead of letting it stop the probe.
  function guard(name, fn) {
    try {
      return fn();
    } catch (e) {
      report.errors.push(name + ': ' + describe(e));
      return null;
    }
  }

  // lookup follows a dotted path from the global object.
  function lookup(path) {
    var parts = path.split('.');
    var value = window;
    for (var i = 0; i < parts.length; i++) {
      if (value === null || value === undefined) {
        return undefined;
      }
      value = value[parts[i]];
    }
    return value;
  }

  function presence(paths) {
    var result = {};
    for (var i = 0; i < paths.length; i++) {
      var value;
      try {
        value = lookup(paths[i]);
      } catch (e) {
        value = undefined;
      }
      result[paths[i]] = value !== null && value !== undefined;
    }
    return result;
  }

  function compiles(source) {
    try {
      Function(source);
      return true;
    } catch (e) {
      return false;
    }
  }

  function probeEnvironment() {
    var root = document.documentElement;
    return {
      user_agent: navigator.userAgent,
      platform: navigator.platform,
      language: navigator.language || navigator.userLanguage || null,
      timezone_offset_minutes: new Date().getTimezoneOffset(),
      screen: {
        width: screen.width,
        height: screen.height,
        avail_width: screen.availWidth,
        avail_height: screen.availHeight,
        color_depth: screen.colorDepth
      },
      window: {
        inner_width: window.innerWidth,
        inner_height: window.innerHeight,
        client_width: root.clientWidth,
        client_height: root.clientHeight,
        device_pixel_ratio: window.devicePixelRatio || null
      }
    };
  }

  function probeSemantics() {
    return {
      strict_mode: (function () { return this === undefined; }()),
      accessors: guard('accessors', function () {
        return Function('return ({ get a() { return 1; } }).a === 1;')();
      }) === true
    };
  }

  function probeSyntax() {
    var result = {};
    for (var name in NEWER_SYNTAX) {
      if (NEWER_SYNTAX.hasOwnProperty(name)) {
        result[name] = compiles(NEWER_SYNTAX[name]);
      }
    }
    return result;
  }

  function probeDom() {
    var root = document.documentElement;
    return {
      compat_mode: document.compatMode,
      query_selector: typeof document.querySelector !== 'undefined',
      add_event_listener: typeof document.addEventListener !== 'undefined',
      get_elements_by_class_name: typeof document.getElementsByClassName !== 'undefined',
      class_list: 'classList' in root,
      dataset: 'dataset' in root,
      text_content: 'textContent' in root,
      inner_text: 'innerText' in root,
      bounding_client_rect: typeof root.getBoundingClientRect !== 'undefined'
    };
  }

  function newStyle() {
    return document.createElement('div').style;
  }

  // firstProperty returns the first style property the browser knows, or null.
  function firstProperty(names) {
    var style = newStyle();
    for (var i = 0; i < names.length; i++) {
      if (typeof style[names[i]] === 'string') {
        return names[i];
      }
    }
    return null;
  }

  // firstValue returns the first value the browser accepts for a style
  // property, or null.
  function firstValue(property, values) {
    for (var i = 0; i < values.length; i++) {
      var style = newStyle();
      try {
        style[property] = values[i];
      } catch (e) {
        continue;
      }
      if (style[property]) {
        return values[i];
      }
    }
    return null;
  }

  function probeCss() {
    return {
      flexbox: firstValue('display', ['flex', '-webkit-flex', '-webkit-box']),
      transform: firstProperty(['transform', 'webkitTransform']),
      transition: firstProperty(['transition', 'webkitTransition']),
      animation: firstProperty(['animation', 'webkitAnimation']),
      border_radius: firstProperty(['borderRadius', 'webkitBorderRadius']),
      box_shadow: firstProperty(['boxShadow', 'webkitBoxShadow']),
      box_sizing: firstProperty(['boxSizing', 'webkitBoxSizing']),
      opacity: firstProperty(['opacity']),
      text_overflow: firstProperty(['textOverflow']),
      rgba: firstValue('color', ['rgba(1, 2, 3, 0.5)']),
      calc: firstValue('width', ['calc(10px + 10px)', '-webkit-calc(10px + 10px)']),
      linear_gradient: firstValue('backgroundImage', [
        'linear-gradient(red, blue)',
        '-webkit-linear-gradient(red, blue)',
        '-webkit-gradient(linear, left top, left bottom, from(red), to(blue))'
      ]),
      position_fixed: firstValue('position', ['fixed']),
      grid: firstValue('display', ['grid', '-webkit-grid', '-ms-grid']),
      rem_units: firstValue('width', ['1rem']),
      viewport_units: firstValue('width', ['10vw'])
    };
  }

  function probeStore(name) {
    try {
      var store = window[name];
      if (!store) {
        return 'missing';
      }
      store.setItem('overscan-probe', '1');
      var ok = store.getItem('overscan-probe') === '1';
      store.removeItem('overscan-probe');
      return ok ? 'ok' : 'did not keep a value';
    } catch (e) {
      return 'error: ' + describe(e);
    }
  }

  function probeStorage() {
    return {
      local_storage: probeStore('localStorage'),
      session_storage: probeStore('sessionStorage'),
      cookies_enabled: navigator.cookieEnabled === true,
      indexed_db: presence(['indexedDB', 'webkitIndexedDB']),
      web_sql: presence(['openDatabase'])
    };
  }

  // probeConstructors checks that constructors whose presence version 1
  // found also work.
  function probeConstructors() {
    return {
      url: guard('URL constructor', function () {
        return new URL('http://a.example/b?c=d').search === '?c=d';
      }),
      custom_event: guard('CustomEvent constructor', function () {
        return new CustomEvent('x', { detail: 1 }).detail === 1;
      })
    };
  }

  // readCookie returns the value of a cookie that the page can see, or null.
  function readCookie(name) {
    var parts = document.cookie ? document.cookie.split('; ') : [];
    for (var i = 0; i < parts.length; i++) {
      var eq = parts[i].indexOf('=');
      if (eq > 0 && parts[i].substring(0, eq) === name) {
        return decodeURIComponent(parts[i].substring(eq + 1));
      }
    }
    return null;
  }

  // probePersistence reads the markers that the previous run left in each
  // kind of storage, then leaves new ones naming this run. Comparing two
  // reports with a reboot between them shows which storage survives it. The
  // server also sets an HttpOnly cookie on the page and reports whether it
  // came back.
  function probePersistence() {
    var mark = 'run at ' + new Date().toISOString();
    var found = {};
    var wrote = {};
    var stb = window.gSTB;

    found.stb_storage = guard('read stbStorage', function () {
      return window.stbStorage ? window.stbStorage.getItem(MARK) : 'missing';
    });
    found.js_cookie = guard('read cookie', function () { return readCookie(MARK); });
    found.user_data_file = guard('read user data', function () {
      return stb && stb.LoadUserData ? stb.LoadUserData(MARK_FILE) : 'missing';
    });
    found.session_storage = guard('read sessionStorage', function () {
      return window.sessionStorage ? window.sessionStorage.getItem(MARK) : 'missing';
    });

    wrote.stb_storage = guard('write stbStorage', function () {
      if (!window.stbStorage) {
        return false;
      }
      window.stbStorage.setItem(MARK, mark);
      return window.stbStorage.getItem(MARK) === mark;
    });
    wrote.js_cookie = guard('write cookie', function () {
      var expires = new Date(new Date().getTime() + COOKIE_YEARS * 365 * 24 * 3600 * 1000);
      document.cookie = MARK + '=' + encodeURIComponent(mark) + '; expires=' + expires.toUTCString() + '; path=/';
      return readCookie(MARK) === mark;
    });
    wrote.user_data_file = guard('write user data', function () {
      if (!stb || !stb.SaveUserData) {
        return false;
      }
      stb.SaveUserData(MARK_FILE, mark);
      return stb.LoadUserData(MARK_FILE) === mark;
    });
    wrote.session_storage = guard('write sessionStorage', function () {
      if (!window.sessionStorage) {
        return false;
      }
      window.sessionStorage.setItem(MARK, mark);
      return window.sessionStorage.getItem(MARK) === mark;
    });

    return { this_run: mark, found: found, wrote: wrote };
  }

  function probeXhr() {
    if (typeof XMLHttpRequest === 'undefined') {
      return { present: false };
    }
    var request = new XMLHttpRequest();
    var result = {
      present: true,
      with_credentials: 'withCredentials' in request,
      override_mime_type: typeof request.overrideMimeType !== 'undefined',
      upload: 'upload' in request,
      response_type: 'responseType' in request
    };
    if (result.response_type) {
      request.open('GET', 'index.html', true);
      result.response_type_json = acceptsResponseType(request, 'json');
      result.response_type_arraybuffer = acceptsResponseType(request, 'arraybuffer');
      request.abort();
    }
    return result;
  }

  function acceptsResponseType(request, type) {
    try {
      request.responseType = type;
      return request.responseType === type;
    } catch (e) {
      return false;
    }
  }

  // call calls a method on a host object. Bridged methods may not be real
  // functions, so it avoids apply.
  function call(object, name, arg) {
    if (typeof object[name] === 'undefined') {
      return { missing: true };
    }
    try {
      return arg === undefined ? object[name]() : object[name](arg);
    } catch (e) {
      return { error: describe(e) };
    }
  }

  function memberNames(object) {
    var names = [];
    try {
      for (var key in object) {
        names.push(key);
      }
    } catch (e) {
      report.errors.push('gSTB members: ' + describe(e));
    }
    names.sort();
    return names;
  }

  function probeMag() {
    var objects = {};
    for (var i = 0; i < MAG_OBJECTS.length; i++) {
      objects[MAG_OBJECTS[i]] = typeof window[MAG_OBJECTS[i]];
    }
    var stb = window.gSTB;
    if (stb === undefined || stb === null) {
      return { objects: objects, gstb: null };
    }
    var info = {};
    for (var j = 0; j < GSTB_GETTERS.length; j++) {
      info[GSTB_GETTERS[j]] = call(stb, GSTB_GETTERS[j]);
    }
    return {
      objects: objects,
      gstb: { members: memberNames(stb), info: info, env: call(stb, 'GetEnv', GSTB_ENV) }
    };
  }

  // probeImages loads one image per format and calls done with the results
  // once every image has loaded, failed or timed out.
  function probeImages(done) {
    var results = {};
    var names = [];
    for (var name in IMAGES) {
      if (IMAGES.hasOwnProperty(name)) {
        names.push(name);
      }
    }
    var pending = names.length;

    function load(key) {
      var image = new Image();
      var settled = false;
      function finish(result) {
        if (settled) {
          return;
        }
        settled = true;
        results[key] = result;
        pending--;
        if (pending === 0) {
          done(results);
        }
      }
      image.onload = function () {
        finish(image.width > 0 ? 'loaded' : 'loaded with no size');
      };
      image.onerror = function () {
        finish('failed');
      };
      setTimeout(function () {
        finish('timed out');
      }, IMAGE_TIMEOUT_MS);
      image.src = IMAGES[key];
    }

    for (var i = 0; i < names.length; i++) {
      load(names[i]);
    }
  }

  // toJSON serialises with JSON.stringify where it exists, and with a small
  // fallback where it does not.
  function toJSON(value) {
    if (window.JSON && typeof JSON.stringify === 'function') {
      return JSON.stringify(value);
    }
    return serialise(value);
  }

  function serialise(value) {
    var parts = [];
    if (value === null || value === undefined || typeof value === 'function') {
      return 'null';
    }
    if (typeof value === 'boolean') {
      return value ? 'true' : 'false';
    }
    if (typeof value === 'number') {
      return isFinite(value) ? String(value) : 'null';
    }
    if (typeof value === 'string') {
      return quote(value);
    }
    if (Object.prototype.toString.call(value) === '[object Array]') {
      for (var i = 0; i < value.length; i++) {
        parts.push(serialise(value[i]));
      }
      return '[' + parts.join(',') + ']';
    }
    for (var key in value) {
      if (Object.prototype.hasOwnProperty.call(value, key)) {
        parts.push(quote(key) + ':' + serialise(value[key]));
      }
    }
    return '{' + parts.join(',') + '}';
  }

  function quote(text) {
    return '"' + String(text).replace(/[\\"\u0000-\u001f\u2028\u2029]/g, function (c) {
      var code = c.charCodeAt(0).toString(16);
      return '\\u' + '0000'.slice(code.length) + code;
    }) + '"';
  }

  function send(done) {
    var request = new XMLHttpRequest();
    request.open('POST', 'report', true);
    request.setRequestHeader('Content-Type', 'application/json');
    request.onreadystatechange = function () {
      if (request.readyState === 4) {
        done(request.status);
      }
    };
    request.send(toJSON(report));
  }

  function setText(element, text) {
    while (element.firstChild) {
      element.removeChild(element.firstChild);
    }
    element.appendChild(document.createTextNode(text));
  }

  function setStatus(text) {
    setText(document.getElementById('status'), text);
  }

  // text turns a report value into something readable on a TV.
  function text(value) {
    if (value === null || value === undefined) {
      return 'unknown';
    }
    if (typeof value === 'object') {
      if (value.missing) {
        return 'not available';
      }
      if (value.error) {
        return 'error: ' + value.error;
      }
    }
    return String(value);
  }

  // foundText describes a marker read from storage: null means the storage
  // held none, as on the first run.
  function foundText(value) {
    return value === null ? 'nothing' : text(value);
  }

  function list(map, wanted) {
    var names = [];
    for (var name in map) {
      if (map.hasOwnProperty(name) && map[name] === wanted) {
        names.push(name);
      }
    }
    return names.length ? names.join(', ') : 'none';
  }

  function addRow(table, label, value) {
    var row = document.createElement('tr');
    var head = document.createElement('th');
    var cell = document.createElement('td');
    head.appendChild(document.createTextNode(label));
    cell.appendChild(document.createTextNode(text(value)));
    row.appendChild(head);
    row.appendChild(cell);
    table.appendChild(row);
  }

  function summarise() {
    var table = document.getElementById('summary');
    var env = report.environment || {};
    var mag = report.mag || {};
    var css = report.css || {};
    var storage = report.storage || {};
    var images = report.images || {};
    var info = (mag.gstb && mag.gstb.info) || {};

    addRow(table, 'Browser', env.user_agent);
    if (mag.gstb) {
      addRow(table, 'Device', text(info.GetDeviceModel) + ' (' + text(info.GetDeviceModelExt) + ')');
      addRow(table, 'Firmware', text(info.GetDeviceImageDesc) + ', ' + text(info.GetDeviceImageVersionCurrent));
      addRow(table, 'JS API', info.Version);
      addRow(table, 'Video settings', mag.gstb.env);
    } else {
      addRow(table, 'Device', 'no gSTB object, so not a MAG box or emulator');
    }
    if (env.screen && env.window) {
      addRow(table, 'Screen', env.screen.width + ' x ' + env.screen.height +
        ', window ' + env.window.inner_width + ' x ' + env.window.inner_height);
    }
    addRow(table, 'Missing ES5', list(report.es5_builtins || {}, false));
    addRow(table, 'Newer features', list(report.newer_builtins || {}, true));
    addRow(table, 'Newer syntax', list(report.newer_syntax || {}, true));
    addRow(table, 'CSS', 'flexbox ' + text(css.flexbox) + ', transform ' + text(css.transform) +
      ', calc ' + text(css.calc) + ', gradients ' + text(css.linear_gradient));
    addRow(table, 'Images', 'loaded: ' + list(images, 'loaded'));
    addRow(table, 'Storage', 'localStorage ' + text(storage.local_storage) + ', sessionStorage ' + text(storage.session_storage));
    var persistence = report.persistence || {};
    var found = persistence.found || {};
    addRow(table, 'From the last run', 'stbStorage ' + foundText(found.stb_storage) + '; cookie ' + foundText(found.js_cookie) +
      '; user data file ' + foundText(found.user_data_file) + '; sessionStorage ' + foundText(found.session_storage));
    addRow(table, 'Errors', report.errors.length ? report.errors.join('; ') : 'none');
  }

  function finish() {
    guard('summary', summarise);
    setStatus('Sending the report...');
    var sent = guard('send', function () {
      send(function (status) {
        setStatus(status === 204 ?
          'Report saved on the server. You can switch the portal back now.' :
          'The server answered ' + status + ', so the report was not saved.');
      });
      return true;
    });
    if (!sent) {
      setStatus('Could not send the report: ' + report.errors[report.errors.length - 1]);
    }
  }

  report.environment = guard('environment', probeEnvironment);
  report.es5_builtins = guard('es5 built-ins', function () { return presence(ES5_BUILTINS); });
  report.es5_semantics = guard('es5 semantics', probeSemantics);
  report.newer_builtins = guard('newer built-ins', function () { return presence(NEWER_BUILTINS); });
  report.newer_syntax = guard('newer syntax', probeSyntax);
  report.dom = guard('dom', probeDom);
  report.css = guard('css', probeCss);
  report.storage = guard('storage', probeStorage);
  report.xhr = guard('xhr', probeXhr);
  report.mag = guard('mag', probeMag);
  report.constructors = guard('constructors', probeConstructors);
  report.persistence = guard('persistence', probePersistence);

  var started = guard('images', function () {
    probeImages(function (images) {
      report.images = images;
      finish();
    });
    return true;
  });
  if (!started) {
    finish();
  }
}());
