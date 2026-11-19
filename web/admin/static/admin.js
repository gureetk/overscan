// SPDX-License-Identifier: AGPL-3.0-or-later
// Small helps for the admin's pages, which work without them: filtering
// the lineup's groups, ticking every channel, marking the channel open in
// the panel, closing menus, and choosing a group on a phone. Listeners sit
// on the document, so they keep working when htmx replaces part of a page.
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
