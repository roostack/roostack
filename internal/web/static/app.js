// Roostack's own small script, for the parts of pages that refresh
// themselves with htmx (the home page, Services, Backups, the drive list).
// They only poll while the tab is visible (see their hx-trigger).
(function () {
  // Each refresh sends a fingerprint of what the part shows; the server
  // answers 204 No Content when nothing changed, and htmx leaves the page
  // alone (renderLive in internal/web/pages.go).
  document.addEventListener("htmx:configRequest", (e) => {
    const fp = e.detail.elt.dataset.fingerprint;
    if (fp) e.detail.headers["X-Roostack-Have"] = fp;
  });
  // A part that replaces itself (hx-swap="outerHTML") is a new element by
  // now: the fingerprint goes on the one with its id.
  document.addEventListener("htmx:afterRequest", (e) => {
    const fp = e.detail.xhr && e.detail.xhr.getResponseHeader("X-Roostack-Fingerprint");
    if (!fp || !e.detail.successful) return;
    e.detail.elt.dataset.fingerprint = fp;
    const now = e.detail.elt.id && document.getElementById(e.detail.elt.id);
    if (now) now.dataset.fingerprint = fp;
  });

  // When a part does change, replacing its HTML would close any <details>
  // panel someone opened: panels with an id stay as they were.
  let open = [];
  document.addEventListener("htmx:beforeSwap", (e) => {
    open = [...e.detail.target.querySelectorAll("details[open][id]")].map((d) => d.id);
    if (e.detail.target.matches("details[open][id]")) open.push(e.detail.target.id);
  });
  document.addEventListener("htmx:afterSwap", () => {
    for (const id of open) {
      const d = document.getElementById(id);
      if (d) d.open = true;
    }
    open = [];
  });

  // Bars (the space-bar) give their widths as data-width (percent): the
  // Content-Security-Policy blocks style attributes, but not styles set
  // from this script. Applied at load and to every part htmx swaps in.
  const widths = (root) => {
    for (const el of root.querySelectorAll("[data-width]")) el.style.width = `${Number(el.dataset.width) || 0}%`;
    // Columns (the bar-chart) give their heights the same way.
    for (const el of root.querySelectorAll("[data-height]")) el.style.height = `${Number(el.dataset.height) || 0}%`;
  };
  document.addEventListener("DOMContentLoaded", () => widths(document));
  document.addEventListener("htmx:load", (e) => widths(e.detail.elt));

  // A refreshed status line is a new element, whose spinner would start
  // its turn again (a visible hitch). Every spinner turns in step with the
  // page's clock instead, so a new one carries on where the old one was.
  const spinTurn = 800; // ms, as .spinner's animation in app.css
  const spinners = (root) => {
    const delay = `-${Math.round(performance.now() % spinTurn)}ms`;
    for (const el of root.querySelectorAll(".spinner")) el.style.animationDelay = delay;
    if (root.matches && root.matches(".spinner")) root.style.animationDelay = delay;
  };
  document.addEventListener("DOMContentLoaded", () => spinners(document));
  document.addEventListener("htmx:load", (e) => spinners(e.detail.elt));

  // The sidebar's links scroll when the window is short (Settings lists
  // all its pages while you're in it): show where you are.
  document.addEventListener("DOMContentLoaded", () => {
    const here = document.querySelector(".sidebar-nav [aria-current=page]");
    if (here) here.scrollIntoView({ block: "nearest" });
  });

  // roostackSize formats bytes as units.Bytes does (decimal: 1 GB = 1000³
  // bytes), for figures that change as someone types.
  window.roostackSize = (n) => {
    if (!(n >= 0)) return "unknown";
    if (n < 1000) return `${Math.floor(n)} B`;
    let div = 1000, exp = 0;
    for (let m = Math.floor(n / 1000); m >= 1000 && exp < 4; m = Math.floor(m / 1000)) { div *= 1000; exp++; }
    return `${(n / div).toFixed(1)} ${"kMGTP"[exp]}B`;
  };

  // roostackCopy copies an element's text (the copy-box component). The
  // browser's clipboard API needs HTTPS (or localhost); elsewhere the text
  // is selected and copied the older way.
  window.roostackCopy = (el) => {
    const text = el.textContent.trim();
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(text);
    const range = document.createRange();
    range.selectNodeContents(el);
    const sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
    return document.execCommand("copy") ? Promise.resolve() : Promise.reject(new Error("copy failed"));
  };

  // A form in a dialog (dialog.form-dialog): a link with data-dialog="<id>"
  // opens it, its data-close links close it; without scripts they're
  // links to the page with it open (as a card), and without it. One the
  // page opened (a form that came back with a mistake) becomes a dialog.
  // With data-load="<selector>" too, the dialog's .dialog-body is filled
  // with that part of the page the link leads to (the form's own page)
  // as it opens: for a form that asks the server about drives, which
  // would slow every page it's on (adding a media library).
  document.addEventListener("DOMContentLoaded", () => {
    for (const d of document.querySelectorAll("dialog.form-dialog[open]")) {
      d.close();
      d.showModal();
    }
  });
  document.addEventListener("click", (e) => {
    const open = e.target.closest("a[data-dialog]");
    const dialog = open && document.getElementById(open.dataset.dialog);
    if (dialog) {
      e.preventDefault();
      const body = open.dataset.load && dialog.querySelector(".dialog-body");
      if (body && window.htmx) {
        body.replaceChildren(Object.assign(document.createElement("p"), { className: "hint", textContent: "Looking at the drives…" }));
        htmx.ajax("GET", open.href, { target: body, select: open.dataset.load, swap: "innerHTML ignoreTitle:true" }).then(() => {
          if (!body.querySelector(open.dataset.load)) body.firstChild.textContent = "Roostack couldn't show this. Close it and try again.";
        });
      }
      dialog.showModal();
      return;
    }
    const close = e.target.closest("dialog.form-dialog [data-close]");
    if (close) {
      e.preventDefault();
      close.closest("dialog").close();
    }
  });

  // The folder picker (a media library's folder): a link with
  // data-picker="<dialog id>" opens that dialog and shows the page it links
  // to (#folder-picker of it) inside; the picker's own links then open
  // folders in the dialog, and its data-use link loads the add page's
  // part named by the dialog's data-place (the folder chosen) in place of
  // the page's. Without scripts the same links are pages of their own.
  const pickerLoad = (dialog, href) => {
    const body = dialog.querySelector(".picker-body");
    body.replaceChildren(Object.assign(document.createElement("p"), { className: "hint", textContent: "Looking at the folders…" }));
    return htmx.ajax("GET", href, { target: body, select: "#folder-picker", swap: "innerHTML ignoreTitle:true" }).then(() => {
      if (!body.querySelector("#folder-picker")) body.firstChild.textContent = "Roostack couldn't list the folders. Close this and try again.";
      const first = body.querySelector(".picker-list a, [data-use]");
      if (first) first.focus();
    });
  };
  document.addEventListener("click", (e) => {
    const open = e.target.closest("a[data-picker]");
    if (open) {
      const dialog = document.getElementById(open.dataset.picker);
      if (!dialog || !window.htmx) return;
      e.preventDefault();
      dialog.showModal();
      pickerLoad(dialog, open.href);
      return;
    }
    const dialog = e.target.closest("dialog.picker");
    if (!dialog) return;
    if (e.target.closest(".picker-close, [data-cancel]")) {
      e.preventDefault();
      dialog.close();
      return;
    }
    const link = e.target.closest(".picker-body a[href]");
    if (!link) return;
    e.preventDefault();
    if (link.matches("[data-use]")) {
      const place = dialog.dataset.place;
      htmx.ajax("GET", link.href, { target: place, select: place, swap: "outerHTML ignoreTitle:true" }).then(() => dialog.close());
    } else {
      pickerLoad(dialog, link.href);
    }
  });

  // The Updates page (SPEC.md §7.1, form.update-choice): the count of
  // ticked updates and the shortcuts to choose fix releases, all, or none;
  // ticking an update with notes opens them first (data-notes), and it's
  // ticked once they've been read (its read.<service> field says so);
  // Update now asks first (data-confirm-updates), listing what will
  // update on each server. Without scripts the form still works, and the
  // server asks for notes to be read.
  const updateForm = () => document.querySelector("form.update-choice");
  const updateBoxes = (form) => [...form.querySelectorAll('input[name="update"]:not(:disabled)')];
  const updateCount = (form) => {
    const n = updateBoxes(form).filter((b) => b.checked).length;
    const label = form.querySelector("[data-chosen-count]");
    if (label) label.textContent = n === 0 ? "Nothing chosen" : n === 1 ? "1 update chosen" : n + " updates chosen";
    for (const b of form.querySelectorAll('button[name="when"]')) b.disabled = n === 0;
  };
  const markRead = (form, name, read) => {
    const f = form.querySelector('[data-read="' + CSS.escape(name) + '"]');
    if (f) f.value = read ? "1" : "";
  };
  document.addEventListener("DOMContentLoaded", () => {
    const form = updateForm();
    if (!form) return;
    const shortcuts = form.querySelector(".choose-shortcuts");
    if (shortcuts) shortcuts.hidden = false;
    updateCount(form);
  });
  document.addEventListener("change", (e) => {
    const form = e.target.closest("form.update-choice");
    if (!form) return;
    if (e.target.name === "update" && !e.target.checked && e.target.dataset.notes) markRead(form, e.target.value, false);
    updateCount(form);
  });
  document.addEventListener("click", (e) => {
    const form = updateForm();
    if (!form) return;
    const box = e.target.closest('form.update-choice input[name="update"][data-notes]');
    if (box && box.checked && form.querySelector('[data-read="' + CSS.escape(box.value) + '"]').value !== "1") {
      e.preventDefault(); // stays unticked until the notes are read
      const d = document.getElementById(box.dataset.notes);
      d.querySelector("[data-read-ok]").checked = false;
      d.querySelector("[data-pick]").disabled = true;
      d.showModal();
      return;
    }
    const pick = e.target.closest("dialog.update-notes [data-pick]");
    if (pick) {
      const name = pick.dataset.pick;
      markRead(form, name, true);
      const b = form.querySelector('input[name="update"][value="' + CSS.escape(name) + '"]');
      b.checked = true;
      pick.closest("dialog").close();
      updateCount(form);
      return;
    }
    const choose = e.target.closest("form.update-choice [data-choose]");
    if (choose) {
      for (const b of updateBoxes(form)) {
        const fix = b.dataset.kind === "patch" || b.dataset.kind === "revision";
        const read = !b.dataset.notes || form.querySelector('[data-read="' + CSS.escape(b.value) + '"]').value === "1";
        b.checked = choose.dataset.choose !== "none" && read && (choose.dataset.choose === "all" || fix);
      }
      updateCount(form);
      return;
    }
    const now = e.target.closest("form.update-choice [data-confirm-updates]");
    if (now) {
      const chosen = updateBoxes(form).filter((b) => b.checked);
      if (chosen.length === 0) return;
      e.preventDefault();
      const d = document.getElementById(now.dataset.confirmUpdates);
      d.querySelector("[data-confirm-title]").textContent =
        chosen.length === 1 ? "Update " + chosen[0].dataset.title + " now?" : "Update " + chosen.length + " services now?";
      const byServer = new Map();
      for (const b of chosen) {
        const s = b.dataset.server;
        if (!byServer.has(s)) byServer.set(s, []);
        byServer.get(s).push(b.dataset.title + " (" + b.dataset.version + ")");
      }
      const list = d.querySelector("[data-confirm-list]");
      list.replaceChildren(...[...byServer].map(([server, items]) => {
        const li = document.createElement("li");
        if (server) li.append(Object.assign(document.createElement("strong"), { textContent: "On " + server + ": " }));
        li.append(listAnd(items));
        return li;
      }));
      const unsaved = chosen.filter((b) => b.hasAttribute("data-no-backups"));
      const servers = new Set(unsaved.map((b) => b.dataset.server)).size;
      d.querySelector("[data-confirm-plan]").textContent = (unsaved.length === chosen.length ? "Roostack will update " : "Roostack will back up each server first, then update ") +
        (chosen.length === 1 ? "it" : "them one at a time") + ". " + (chosen.length === 1 ? "It" : "Each one") + " will be unavailable for a minute or two while it switches over.";
      const warn = d.querySelector("[data-confirm-no-backups]");
      warn.hidden = unsaved.length === 0;
      warn.textContent = unsaved.length === 0 ? "" : "Roostack can't back up " + listAnd(unsaved.map((b) => b.dataset.title)) + " first, since " +
        (unsaved.length === 1 ? "its server has" : servers === 1 ? "their server has" : "their servers have") +
        " no backups set up: if an update changes the data, there's no copy to go back to.";
      d.showModal();
      return;
    }
    const readOk = e.target.closest("dialog.update-notes [data-read-ok]");
    if (readOk) readOk.closest("dialog").querySelector("[data-pick]").disabled = !readOk.checked;
  });
  // A list in a sentence, with the Oxford comma: "A", "A and B", "A, B, and C".
  const listAnd = (items) =>
    items.length < 3 ? items.join(" and ") : items.slice(0, -1).join(", ") + ", and " + items[items.length - 1];
})();
