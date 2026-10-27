"use strict";

// Browser side of "bible gui". The server does all parsing and resolution;
// this file keeps the dropdowns in sync and renders results.

const $ = (id) => document.getElementById(id);
const tSel = $("translation");
const bookSel = $("book");
const chapterSel = $("chapter");
const lookupEdit = $("lookup");
const searchEdit = $("search");
const regexBox = $("regex");
const icaseBox = $("icase");
const textView = $("text");

// Books (with chapter lists) of the translation currently in tSel.
let books = [];
// Whatever book/chapter the dropdowns last pointed at, so a translation
// change can stay in the same place instead of resetting to book 1.
let lastBook = null;
let lastChapter = null;
// Incremented for every display request, so a slow response can't
// overwrite a newer one.
let displaySeq = 0;

async function api(path, params = {}) {
  const url = new URL(path, location.href);
  for (const [k, v] of Object.entries(params)) url.searchParams.set(k, v);
  const resp = await fetch(url);
  if (!resp.ok) throw new Error((await resp.text()).trim() || resp.statusText);
  return resp.json();
}

function fillSelect(sel, items) {
  sel.replaceChildren(...items.map(([value, label]) => new Option(label, value)));
}

// Select the option with this value, if present. Assigning .value never
// fires "change", so this syncs state without triggering a refresh.
function selectValue(sel, value) {
  value = String(value);
  if (![...sel.options].some((o) => o.value === value)) return false;
  sel.value = value;
  return true;
}

async function loadBooks(table) {
  books = (await api("/api/books", { t: table })).books;
  fillSelect(bookSel, books.map((b) => [b.name, b.name]));
}

function fillChapters(bookName) {
  const book = books.find((b) => b.name === bookName);
  const chapters = book ? book.chapters : [];
  fillSelect(chapterSel, chapters.map((c) => [String(c), String(c)]));
}

// ---------- rendering ----------

function showMessage(text) {
  const p = document.createElement("p");
  p.className = "msg";
  p.textContent = text;
  textView.replaceChildren(p);
  textView.scrollTop = 0;
}

function showError(err) {
  showMessage(`Error: ${err.message || err}`);
}

function appendText(p, row) {
  if (!row.segments) {
    // An empty text is left out of the JSON.
    p.append(row.text ?? "");
    return;
  }
  for (const seg of row.segments) {
    if (seg.hl) {
      const b = document.createElement("b");
      b.className = "hit";
      b.textContent = seg.text;
      p.append(b);
    } else {
      p.append(seg.text);
    }
  }
}

function render(result) {
  if (result.message) {
    showMessage(result.message);
    return;
  }
  const frag = document.createDocumentFragment();
  let lastTrans = null;
  let p = null;
  result.rows.forEach((row, i) => {
    // A verse continuing a paragraph goes on the same line as "(N) text".
    if (row.cont && p) {
      const num = document.createElement("span");
      num.className = "num";
      num.textContent = `(${row.verse})`;
      p.append(" ", num, " ");
      appendText(p, row);
      return;
    }
    if (row.translation !== lastTrans) {
      lastTrans = row.translation;
      if (i > 0) {
        const gap = document.createElement("div");
        gap.className = "gap";
        frag.append(gap);
      }
    }
    p = document.createElement("p");
    const ref = document.createElement("span");
    ref.className = "ref";
    const prefix = result.multi ? row.translation.toUpperCase() + " " : "";
    // Label the paragraph with its full verse range, e.g. "Matthew 5:3-6".
    let j = i;
    while (j + 1 < result.rows.length && result.rows[j + 1].cont) j++;
    const last = result.rows[j].verse;
    const range = last !== row.verse ? `${row.verse}-${last}` : `${row.verse}`;
    ref.textContent = `${prefix}${row.book} ${row.chapter}:${range}`;
    p.append(ref, " ");
    appendText(p, row);
    frag.append(p);
  });
  textView.classList.toggle("paras", result.rows.some((row) => row.cont));
  textView.replaceChildren(frag);
  textView.scrollTop = 0;
}

// Run a display request, dropping its result if a newer one has started.
async function display(fetchResult, apply) {
  const seq = ++displaySeq;
  try {
    const result = await fetchResult();
    if (seq !== displaySeq) return;
    if (apply) await apply(result);
    if (seq !== displaySeq) return;
    render(result);
  } catch (err) {
    if (seq === displaySeq) showError(err);
  }
}

// ---------- actions ----------

function showChapter() {
  const t = tSel.value;
  const b = bookSel.value;
  const c = chapterSel.value;
  if (!t || !b || !c) {
    displaySeq++;
    textView.replaceChildren();
    return;
  }
  lastBook = b;
  lastChapter = c;
  display(() => api("/api/chapter", { t, b, c }));
}

// Point the dropdowns at a resolved lookup. A lookup with an explicit
// t:<name> filter also switches the translation (and its book list).
async function syncTo(sync) {
  if (sync.translation !== tSel.value) {
    selectValue(tSel, sync.translation);
    await loadBooks(sync.translation);
  }
  if (!selectValue(bookSel, sync.book)) return;
  fillChapters(sync.book);
  selectValue(chapterSel, sync.chapter);
  lastBook = sync.book;
  lastChapter = String(sync.chapter);
}

function doLookup() {
  const q = lookupEdit.value.trim();
  if (!tSel.value || !q) return;
  display(
    () => api("/api/lookup", { t: tSel.value, q }),
    (result) => (result.sync ? syncTo(result.sync) : null),
  );
}

function doSearch() {
  const q = searchEdit.value.trim();
  if (!tSel.value || !q) return;
  const params = { t: tSel.value, q };
  if (!regexBox.checked) params.fixed = "1";
  if (icaseBox.checked) params.icase = "1";
  display(() => api("/api/search", params));
}

async function onTranslationChanged() {
  const table = tSel.value;
  if (!table) return;
  try {
    await loadBooks(table);
  } catch (err) {
    showError(err);
    return;
  }

  const hasLookup = lookupEdit.value.trim() !== "";
  const hasSearch = searchEdit.value.trim() !== "";
  const browsing = !hasLookup && !hasSearch;

  if (browsing && lastBook !== null) selectValue(bookSel, lastBook);
  fillChapters(bookSel.value);
  if (browsing && lastChapter !== null) selectValue(chapterSel, lastChapter);

  if (hasLookup) doLookup();
  else if (hasSearch) doSearch();
  else showChapter();
}

function onBookChanged() {
  fillChapters(bookSel.value);
  showChapter();
}

// ---------- lifetime ----------

let stopped = false;

function showStopped() {
  if (stopped) return;
  stopped = true;
  events.close();
  $("closed").hidden = false;
}

// The open event stream is what keeps the server running; when the last
// tab closes, the server notices and exits.
const events = new EventSource("/api/events");
events.addEventListener("bye", showStopped);
events.addEventListener("error", () => {
  // EventSource retries on its own; if the server doesn't come back
  // within a few seconds, it has exited.
  setTimeout(() => {
    if (events.readyState !== EventSource.OPEN) showStopped();
  }, 4000);
});

async function quit() {
  try {
    await fetch("/api/quit", { method: "POST" });
  } catch {
    // Already gone.
  }
  showStopped();
}

// ---------- keyboard ----------

function openPicker(sel) {
  sel.focus();
  try {
    sel.showPicker();
  } catch {
    // Not supported here; focusing lets the arrow keys change it.
  }
}

function focusAndSelect(input) {
  input.focus();
  input.select();
}

document.addEventListener("keydown", (e) => {
  const primary = (e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey;
  const alt = e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey;
  let action = null;
  if (primary) {
    action = {
      KeyD: () => textView.focus(),
      KeyB: () => openPicker(bookSel),
      KeyK: () => openPicker(chapterSel),
      KeyL: () => focusAndSelect(lookupEdit),
      KeyS: () => focusAndSelect(searchEdit),
    }[e.code];
  } else if (alt) {
    // Ctrl+T and Ctrl+Q belong to the browser, so these use Alt.
    action = { KeyT: () => openPicker(tSel), KeyQ: quit }[e.code];
  }
  if (action) {
    e.preventDefault();
    action();
  }
});

lookupEdit.addEventListener("keydown", (e) => {
  if (e.key === "Enter") doLookup();
});
for (const box of [regexBox, icaseBox]) {
  box.addEventListener("change", () => {
    if (searchEdit.value.trim() !== "") doSearch();
  });
}
searchEdit.addEventListener("keydown", (e) => {
  if (e.key === "Enter") doSearch();
});
tSel.addEventListener("change", onTranslationChanged);
bookSel.addEventListener("change", onBookChanged);
chapterSel.addEventListener("change", showChapter);
$("quit").addEventListener("click", quit);
$("help").addEventListener("click", () => $("shortcuts").showModal());

// ---------- start ----------

(async function start() {
  try {
    const { translations, default: def } = await api("/api/translations");
    fillSelect(tSel, translations.map((t) => [t.id, t.label]));
    selectValue(tSel, def);
    await onTranslationChanged();
  } catch (err) {
    showError(err);
  }
  lookupEdit.focus();
})();
