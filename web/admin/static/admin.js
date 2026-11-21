// SPDX-License-Identifier: AGPL-3.0-or-later
// Small helps for the admin's pages, which work without them: filtering
// the lineup's groups, ticking every channel, marking the channel open in
// the panel, closing menus and keeping them on the screen, and choosing a
// group on a phone. Listeners sit on the document, so they keep working when
// htmx replaces part of a page.
"use strict";

document.documentElement.classList.add("js");

// Inputs that only work with this script are hidden until it runs.
function reveal(root) {
  for (const el of root.querySelectorAll("[data-js-only]")) {
    el.hidden = false;
  }
}
reveal(document);
document.addEventListener("htmx:afterSettle", () => reveal(document));

// A filter field hides the items of its list whose names lack its text.
document.addEventListener("input", (e) => {
  const input = e.target.closest("input[data-filter]");
  if (!input) {
    return;
  }
  const list = document.querySelector(input.dataset.filter);
  if (!list) {
    return;
  }
  const text = input.value.trim().toLowerCase();
  for (const item of list.querySelectorAll("[data-name]")) {
    item.hidden = text !== "" && !item.dataset.name.toLowerCase().includes(text);
  }
});

// The box at the head of the channels ticks or unticks them all, and shows
// whether some are ticked.
function channelBoxes() {
  return document.querySelectorAll('input[form="bulk"][name="ids"]');
}
document.addEventListener("change", (e) => {
  const all = e.target.closest("input[data-tick-all]");
  if (all) {
    for (const box of channelBoxes()) {
      box.checked = all.checked;
    }
    return;
  }
  if (e.target.matches('input[form="bulk"][name="ids"]')) {
    const head = document.querySelector("input[data-tick-all]");
    if (head) {
      const boxes = [...channelBoxes()];
      const ticked = boxes.filter((b) => b.checked).length;
      head.checked = ticked === boxes.length;
      head.indeterminate = ticked > 0 && ticked < boxes.length;
    }
  }
  // A select that acts as soon as it changes: it submits its form, through
  // the button whose action it names if it names one. A click on that
  // button, hidden or not, is what htmx reads the action from.
  const auto = e.target.closest("select[data-autosubmit]");
  if (auto && auto.form) {
    const action = auto.dataset.autosubmit;
    const button = action ? auto.form.querySelector(`button[name="action"][value="${action}"]`) : null;
    if (button) {
      button.click();
    } else {
      auto.form.requestSubmit();
    }
  }
});
document.addEventListener("reset", (e) => {
  if (e.target.id === "bulk") {
    const head = document.querySelector("input[data-tick-all]");
    if (head) {
      head.checked = false;
      head.indeterminate = false;
    }
  }
});

// Opening a channel in the panel marks its row.
document.addEventListener("click", (e) => {
  const link = e.target.closest("a.entry-link");
  if (link) {
    for (const row of document.querySelectorAll(".channels tr.open")) {
      row.classList.remove("open");
    }
    link.closest("tr")?.classList.add("open");
  }
  // A click outside an open menu closes it.
  for (const menu of document.querySelectorAll("details.popover[open]")) {
    if (!menu.contains(e.target)) {
      menu.open = false;
    }
  }
});

// Escape closes the open menus, and gives the focus back to their buttons.
document.addEventListener("keydown", (e) => {
  if (e.key !== "Escape") {
    return;
  }
  for (const menu of document.querySelectorAll("details.popover[open]")) {
    menu.open = false;
    menu.querySelector("summary")?.focus();
  }
});

// An open menu stays on the screen, and no panel around it cuts it off: it
// is placed in the window, lined up with its button as the styles line it
// up, moved in from the window's edges, and put above the button when there
// is more room there. It keeps that side while it is open, so that what
// grows in it, such as a search's results, does not make it jump. It
// follows its button as the page scrolls, and closes once the button is out
// of sight. Without this script, a menu opens where the styles put it.
const menuEdge = 8;
const menuGap = 6;
const placedStyles = ["position", "top", "left", "right", "bottom", "maxHeight", "overflowY"];

function unplace(menu) {
  for (const p of placedStyles) {
    menu.style[p] = "";
  }
}

function forget(menu) {
  unplace(menu);
  delete menu.dataset.side;
}

// Whether a box is out of sight: off the window, or scrolled out of a panel.
function outOfSight(el) {
  const r = el.getBoundingClientRect();
  if (r.width === 0 && r.height === 0) {
    return true;
  }
  const vw = document.documentElement.clientWidth;
  const vh = document.documentElement.clientHeight;
  if (r.bottom <= 0 || r.top >= vh || r.right <= 0 || r.left >= vw) {
    return true;
  }
  for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
    const cs = getComputedStyle(a);
    if (cs.overflowX === "visible" && cs.overflowY === "visible") {
      continue;
    }
    const p = a.getBoundingClientRect();
    if (r.bottom <= p.top || r.top >= p.bottom || r.right <= p.left || r.left >= p.right) {
      return true;
    }
  }
  return false;
}

function placeMenu(details, opening) {
  const summary = details.querySelector(":scope > summary");
  const menu = details.querySelector(":scope > .menu");
  if (!summary || !menu) {
    return;
  }
  unplace(menu);
  if (outOfSight(summary)) {
    details.open = false;
    return;
  }
  const s = summary.getBoundingClientRect();
  // Where the styles put it: under the button or over it, and lined up
  // with its left edge or its right.
  const styled = menu.getBoundingClientRect();
  const alignRight = Math.abs(styled.right - s.right) < Math.abs(styled.left - s.left);
  const up = styled.bottom <= s.top + 1;
  menu.style.position = "fixed";
  menu.style.top = "0px";
  menu.style.left = "0px";
  menu.style.right = "auto";
  menu.style.bottom = "auto";
  // At 0, 0 the menu shows where the window's corner is for it.
  const m = menu.getBoundingClientRect();
  const vw = document.documentElement.clientWidth;
  const vh = document.documentElement.clientHeight;
  let x = alignRight ? s.right - m.width : s.left;
  x = Math.max(menuEdge, Math.min(x, vw - menuEdge - m.width));
  const below = vh - menuEdge - (s.bottom + menuGap);
  const above = s.top - menuGap - menuEdge;
  let goUp = menu.dataset.side === "up";
  if (opening || !menu.dataset.side) {
    goUp = up ? m.height <= above || above > below : m.height > below && above > below;
    menu.dataset.side = goUp ? "up" : "down";
  }
  const room = goUp ? above : below;
  let height = m.height;
  if (height > room) {
    height = Math.max(room, 0);
    menu.style.maxHeight = `${height}px`;
    menu.style.overflowY = "auto";
  }
  const y = goUp ? s.top - menuGap - height : s.bottom + menuGap;
  menu.style.left = `${Math.round(x - m.left)}px`;
  menu.style.top = `${Math.round(y - m.top)}px`;
}

function placeOpenMenus() {
  for (const details of document.querySelectorAll("details.popover[open]")) {
    placeMenu(details);
  }
}

let placing = false;
function placeSoon() {
  if (placing) {
    return;
  }
  placing = true;
  requestAnimationFrame(() => {
    placing = false;
    placeOpenMenus();
  });
}

// A details element's toggle does not bubble, so it is caught on its way down.
document.addEventListener("toggle", (e) => {
  const details = e.target;
  if (!(details instanceof HTMLDetailsElement) || !details.classList.contains("popover")) {
    return;
  }
  if (details.open) {
    placeMenu(details, true);
  } else {
    const menu = details.querySelector(":scope > .menu");
    if (menu) {
      forget(menu);
    }
  }
}, true);
window.addEventListener("resize", placeSoon);
document.addEventListener("scroll", placeSoon, { capture: true, passive: true });
document.addEventListener("htmx:afterSettle", placeSoon);
placeOpenMenus();
