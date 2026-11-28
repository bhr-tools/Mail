// Mail web app: state, actions and orchestration of the views.

import { api, onUnauthorized } from './api.js';
import { h, icon } from './dom.js';
import { clock } from './format.js';
import { closePopover, confirmDialog, openContextMenu, openMenu, openModal, promptDialog, toast, topModal } from '/kit/js/overlay.js';
import { folderLabel, isCustom, navigation, renderSidebar } from './views/sidebar.js';
import { renderList, threadKey, updateRows } from './views/list.js';
import { renderBody, renderReader, scrollToFocused } from './views/reader.js';
import { openCompose } from './views/compose.js';
import { openAddySettings } from './views/addy.js';
import { describeQuery, openSearchPage } from './views/search.js';
import { initKeys } from './keys.js';
import { edgeSwipe, pullToRefresh } from '/kit/js/swipe.js';
import { tabBar } from '/kit/js/tabbar.js';
import { createLock } from '/kit/js/lock.js';
import { createUndo } from '/kit/js/undo.js';
import { openHelp } from '/kit/js/help.js';
import { draggable } from '/kit/js/drag.js';

const COLORS = ['pine', 'rose', 'iris', 'gold', 'foam', 'love'];
const PAGE = 50;
// The reader sits beside the list above the phone layout (style.css).
const WIDE = window.matchMedia('(min-width: 769px)');
// How long a conversation shown by j / k stays before it counts as read.
const READ_AFTER = 1000;
const $ = (id) => document.getElementById(id);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// Per-device preferences; the app works without them (private windows).
const prefs = {
  get(key, fallback) {
    try {
      return localStorage.getItem(`mail.${key}`) ?? fallback;
    } catch {
      return fallback;
    }
  },
  set(key, value) {
    try {
      localStorage.setItem(`mail.${key}`, value);
    } catch { /* not persisted */ }
  },
};

const ACTION_LABELS = {
  done: 'Archived', trash: 'Moved to the trash', inbox: 'Moved to the inbox', read: 'Marked as read',
  unread: 'Marked as unread', star: 'Starred', unstar: 'Star removed', delete: 'Deleted',
};
const MOVES = new Set(['done', 'trash', 'inbox', 'move', 'delete']);

class App {
  constructor() {
    this.state = {
      accounts: [],
      addy: { domain: '', accounts: [] },
      version: 0,
      view: { role: 'inbox', account: prefs.get('account', '') },
      search: null, // { query }
      threads: [],
      more: false,
      loading: true,
      cursor: 0,
      selected: new Set(), // thread keys
      open: null, // key of the open conversation
      reader: null,
    };
    // Accounts showing every folder; the others show their inbox only.
    this.expanded = new Set(readJSON(prefs.get('expanded', '[]')));
    // The last query of the search page, to pick it up again.
    this.lastQuery = '';
    this.listSeq = 0;
    this.watching = false;
    this.handleKey = () => {};
    // u undoes; the server keeps what is needed a day.
    this.undos = createUndo({ toast });
    this.lockScreen = createLock({ appName: 'Mail', onUnlocked: () => this.afterLogin() });
  }

  get authenticated() {
    return this.lockScreen.status.authenticated;
  }

  async start() {
    this.bindChrome();
    initKeys(this);
    onUnauthorized(() => this.showLogin());
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible' && this.authenticated) this.refresh();
    });
    window.addEventListener('hashchange', () => this.handleHash());
    renderReader(this);
    await this.boot();
  }

  async boot() {
    try {
      await this.lockScreen.start();
    } catch (err) {
      this.showFatal(err);
    }
  }

  async afterLogin() {
    document.getElementById('auth-menu').hidden = this.lockScreen.status.no_auth;
    await this.loadAccounts();
    await this.loadThreads();
    this.watch();
    this.handleHash();
  }

  // -------------------------------------------------------------------------
  // Data
  // -------------------------------------------------------------------------

  async loadAccounts() {
    const data = await api.accounts();
    this.state.accounts = data.accounts;
    this.state.addy = data.addy;
    if (!this.state.version) this.state.version = data.version;
    const { view } = this.state;
    // With several accounts, the view is always one of them.
    if (this.state.accounts.length > 1 && !this.accountOf(view.account)) view.account = this.state.accounts[0].id;
    // A folder gone from the server (a label deleted elsewhere).
    if (view.role.startsWith('folder:') && !this.state.accounts.some((a) => a.folders.some((f) => f.role === view.role))) {
      view.role = 'inbox';
    }
    if (this.state.accounts.length === 1) view.account = '';
    this.buildTabs();
    renderSidebar(this);
    this.renderTitle();
    this.renderStatus();
  }

  /** Loads the list; keep reloads the same rows, keeping the cursor. */
  async loadThreads({ keep = false } = {}) {
    const seq = ++this.listSeq;
    const { view, search } = this.state;
    const limit = keep ? Math.min(200, Math.max(PAGE, this.state.threads.length)) : PAGE;
    if (!keep) {
      this.state.loading = true;
      this.state.threads = [];
      renderList(this);
    }
    let list;
    try {
      list = search ? await api.search(search.query, 0, limit) : await api.threads(view.role, view.account, 0, limit);
    } catch (err) {
      if (seq === this.listSeq) toast(err.message, { type: 'error' });
      return;
    }
    if (seq !== this.listSeq) return;
    const before = this.state.threads[this.state.cursor];
    this.state.threads = list;
    this.state.more = list.length === limit;
    this.state.loading = false;
    const keys = new Set(list.map(threadKey));
    for (const k of [...this.state.selected]) if (!keys.has(k)) this.state.selected.delete(k);
    if (before) {
      const i = list.findIndex((t) => threadKey(t) === threadKey(before));
      this.state.cursor = i >= 0 ? i : Math.min(this.state.cursor, Math.max(0, list.length - 1));
    } else {
      this.state.cursor = Math.min(this.state.cursor, Math.max(0, list.length - 1));
    }
    renderList(this);
    this.renderStatus();
    this.follow();
  }

  async loadMore() {
    const { view, search, threads } = this.state;
    const seq = this.listSeq;
    try {
      const list = search ? await api.search(search.query, threads.length) : await api.threads(view.role, view.account, threads.length);
      if (seq !== this.listSeq) return;
      const keys = new Set(threads.map(threadKey));
      this.state.threads = threads.concat(list.filter((t) => !keys.has(threadKey(t))));
      this.state.more = list.length === PAGE;
      renderList(this);
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  /** Reloads what is shown, after a change on the server. */
  async refresh() {
    try {
      await this.loadAccounts();
      await this.loadThreads({ keep: true });
      await this.refreshReader();
    } catch (err) {
      if (err.status !== 401) console.warn(err);
    }
  }

  /** Follows the changes of the mail (new messages, synchronization). */
  async watch() {
    if (this.watching) return;
    this.watching = true;
    while (this.authenticated) {
      try {
        const { version } = await api.changes(this.state.version);
        if (version !== this.state.version) {
          this.state.version = version;
          await this.refresh();
          await sleep(400); // a synchronization changes many things in a row
        }
      } catch (err) {
        if (err.status === 401) break;
        await sleep(5000);
      }
    }
    this.watching = false;
  }

  isExpanded(id) {
    // On a phone the Folders page lists every folder.
    return this.expanded.has(id) || !this.wide();
  }

  /** Moves an account to position index of the sidebar, for good. */
  async moveAccount(id, index) {
    const accounts = this.state.accounts.filter((a) => a.id !== id);
    const moved = this.accountOf(id);
    if (!moved) return;
    accounts.splice(index, 0, moved);
    if (accounts.every((a, i) => a === this.state.accounts[i])) return;
    this.state.accounts = accounts;
    renderSidebar(this);
    try {
      await api.setAccountOrder(accounts.map((a) => a.id));
    } catch (err) {
      toast(err.message, { type: 'error' });
      await this.loadAccounts().catch(() => {});
    }
  }

  toggleExpanded(id) {
    if (this.expanded.has(id)) this.expanded.delete(id);
    else this.expanded.add(id);
    prefs.set('expanded', JSON.stringify([...this.expanded]));
    renderSidebar(this);
  }

  /** +: folds or unfolds the account of the folder being viewed. */
  toggleCurrentSection() {
    const { view, accounts } = this.state;
    const id = view.account || (accounts.length === 1 ? accounts[0].id : '');
    if (id) this.toggleExpanded(id);
  }

  /** Shift+J / Shift+K: next or previous folder of the sidebar. */
  moveFolder(step) {
    const list = navigation(this);
    if (!list.length) return;
    const { view, search } = this.state;
    const i = search ? -1 : list.findIndex((f) => f.role === view.role && f.account === view.account);
    const next = list[Math.max(0, Math.min(list.length - 1, i + step))];
    if (next && (search || next.role !== view.role || next.account !== view.account)) this.setView(next.role, next.account);
  }

  accountOf(id) {
    return this.state.accounts.find((a) => a.id === id);
  }

  colorOf(id) {
    const i = this.state.accounts.findIndex((a) => a.id === id);
    return COLORS[Math.max(0, i) % COLORS.length];
  }

  emptyText() {
    switch (this.state.view.role) {
      case 'inbox': return 'Your inbox is empty.';
      case 'drafts': return 'No draft.';
      case 'trash': return 'The trash is empty.';
      case 'junk': return 'No spam.';
      default: return 'No mail here.';
    }
  }

  // -------------------------------------------------------------------------
  // Rendering of the chrome
  // -------------------------------------------------------------------------

  /** The list the reader goes back to: "Inbox", "Archive", "Search". */
  listName() {
    return this.state.search ? 'Search' : folderLabel(this.state.view.role);
  }

  renderTitle() {
    const { view, search, accounts } = this.state;
    const title = $('list-title');
    if (search) {
      title.replaceChildren('Search ', h('span', { class: 'muted' }, describeQuery(this, search.query)));
    } else {
      const account = this.accountOf(view.account);
      const suffix = accounts.length > 1 && account ? account.name : '';
      // " · INP" beside the folder; under it, alone, on a phone.
      title.replaceChildren(folderLabel(view.role), suffix ? h('span', { class: 'muted list-account' }, suffix) : '');
    }
    document.title = `${search ? 'Search' : folderLabel(view.role)} · Mail`;
    this.renderTabs();
  }

  renderStatus(mode) {
    const el = $('status-mode');
    const label = mode || (document.activeElement?.matches?.('input, textarea, select') ? 'INSERT' : 'NORMAL');
    el.textContent = `-- ${label} --`;
    el.className = `status-mode ${label === 'INSERT' ? 'insert' : ''}`;
    const { threads, cursor, selected } = this.state;
    $('status-position').textContent = threads.length
      ? `${cursor + 1} / ${threads.length}${this.state.more ? '+' : ''}${selected.size ? ` · ${selected.size} selected` : ''}`
      : '';
    const sync = $('status-sync');
    const accounts = this.state.accounts;
    const failed = accounts.filter((a) => a.status.state === 'error');
    const pending = accounts.reduce((n, a) => n + (a.status.pending || 0), 0);
    let text = '';
    let cls = '';
    if (failed.length) {
      text = `${failed.map((a) => a.name).join(', ')}: ${failed[0].status.error}`;
      cls = 'error';
    } else if (accounts.some((a) => a.status.state === 'syncing' || a.status.state === 'connecting')) {
      text = 'Syncing…';
      cls = 'busy';
    } else if (accounts.length) {
      const times = accounts.map((a) => new Date(a.status.synced_at).getTime()).filter((t) => t > 0);
      if (times.length) text = `Synced ${clock(new Date(Math.min(...times)).toISOString())}`;
    }
    if (pending) text += ` · ${pending} waiting for the server`;
    sync.textContent = text;
    sync.className = `status-sync ${cls}`;
    sync.title = failed.map((a) => `${a.name}: ${a.status.error}`).join('\n');
  }

  // -------------------------------------------------------------------------
  // Navigation
  // -------------------------------------------------------------------------

  setView(role, account = this.state.view.account) {
    this.state.view = { role, account: this.state.accounts.length > 1 ? account : '' };
    prefs.set('account', this.state.view.account);
    this.state.search = null;
    this.state.cursor = 0;
    this.state.selected.clear();
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    this.loadThreads();
  }

  /** & é " ': each account, in the current folder. */
  switchAccount(index) {
    const { accounts } = this.state;
    if (accounts.length < 2) return;
    const account = accounts[index]?.id;
    if (account === undefined) return;
    // Another account has other folders of its own.
    const { role } = this.state.view;
    this.setView(this.state.search || role.startsWith('folder:') ? 'inbox' : role, account);
  }

  setCursor(index) {
    const n = this.state.threads.length;
    if (!n) return;
    this.state.cursor = Math.max(0, Math.min(n - 1, index));
    updateRows(this);
    this.renderStatus();
    if (this.state.more && this.state.cursor >= n - 5) this.loadMore();
    this.follow();
  }

  /** Whether the reader sits beside the list (not on a phone). */
  wide() {
    return WIDE.matches;
  }

  /**
   * Beside the list, the reader always shows the conversation under the
   * cursor (a draft too: Enter or e finishes it).
   */
  follow() {
    if (!this.wide()) return;
    const t = this.cursorThread();
    if (!t) {
      if (!this.state.loading) this.closeReader();
      return;
    }
    if (this.state.open !== threadKey(t)) this.openThread(t);
  }

  /** j / k: next or previous conversation; an open reader follows. */
  move(step) {
    const reading = this.state.reader && this.state.open === this.cursorKey();
    this.setCursor(this.state.cursor + step);
    if (reading && !this.wide()) this.openCursor();
  }

  moveTo(index) {
    const reading = this.state.reader && this.state.open === this.cursorKey();
    this.setCursor(index);
    if (reading && !this.wide()) this.openCursor();
  }

  cursorThread() {
    return this.state.threads[this.state.cursor] || null;
  }

  cursorKey() {
    const t = this.cursorThread();
    return t ? threadKey(t) : null;
  }

  openCursor() {
    const t = this.cursorThread();
    if (!t) return;
    // A draft opens where it can be finished.
    if (this.roleOf(t) === 'drafts' && t.count === 1) {
      this.editDraft(t.latest.id);
      return;
    }
    if (this.state.open === threadKey(t)) return;
    this.openThread(t);
  }

  /** Enter: opens the conversation (on a phone), or finishes a draft. */
  enter() {
    this.openCursor();
  }

  async openThread(t) {
    const key = threadKey(t);
    const view = this.state.search ? t.latest.role : this.state.view.role;
    this.state.open = key;
    this.state.reader = {
      key, account: t.account, thread: t.thread, view, subject: t.latest.subject,
      messages: [], focused: 0, expanded: new Set(), cards: new Map(),
    };
    document.body.classList.add('reading');
    updateRows(this);
    renderReader(this);
    $('reader').scrollTop = 0;
    let data;
    try {
      data = await api.thread(t.account, t.thread, view);
    } catch (err) {
      if (this.state.open === key) toast(err.message, { type: 'error' });
      return;
    }
    const r = this.state.reader;
    if (!r || r.key !== key) return;
    r.subject = data.subject;
    // Newest first, as the conversation list.
    r.messages = data.messages.slice().reverse();
    // Every message unfolded; the reading starts at the oldest unread, or
    // at the newest message.
    for (const m of r.messages) r.expanded.add(m.id);
    r.focused = Math.max(0, r.messages.findLastIndex((m) => !m.seen));
    renderReader(this);
    if (r.messages.length > 1) scrollToFocused(this);
    // Opening a conversation reads it. Beside the list, where j / k show
    // each conversation passed over, once it has stayed a moment.
    const read = () => {
      if (!(t.unread > 0) || this.state.open !== key) return;
      t.unread = 0;
      updateRows(this);
      api.act('read', view, { threads: [{ account: t.account, thread: t.thread }] }).catch((err) => toast(err.message, { type: 'error' }));
    };
    if (this.wide()) setTimeout(read, READ_AFTER);
    else read();
  }

  async refreshReader() {
    const r = this.state.reader;
    if (!r || !r.messages.length) return;
    try {
      const data = await api.thread(r.account, r.thread, r.view);
      if (this.state.reader !== r) return;
      const known = new Set(r.messages.map((m) => m.id));
      for (const m of data.messages) if (!known.has(m.id)) r.expanded.add(m.id);
      // The focus stays on its message as new ones arrive on top.
      const focusedId = r.messages[r.focused]?.id;
      r.messages = data.messages.slice().reverse();
      r.focused = Math.max(0, r.messages.findIndex((m) => m.id === focusedId));
      renderReader(this);
    } catch { /* the conversation left this folder: keep what is shown */ }
  }

  closeReader() {
    if (!this.state.reader) return;
    this.state.open = null;
    this.state.reader = null;
    document.body.classList.remove('reading');
    renderReader(this);
    updateRows(this);
  }

  toggleMessage(id) {
    const r = this.state.reader;
    if (!r) return;
    // A click that ends a text selection (an address to copy) keeps the
    // message as it is.
    if (String(window.getSelection?.() || '')) return;
    if (r.expanded.has(id) && r.messages.length > 1) r.expanded.delete(id);
    else r.expanded.add(id);
    r.focused = Math.max(0, r.messages.findIndex((m) => m.id === id));
    renderReader(this);
  }

  focusedMessage() {
    const r = this.state.reader;
    return r?.messages[r.focused] || null;
  }

  focusedCard() {
    const m = this.focusedMessage();
    return m ? this.state.reader.cards.get(m.id) : null;
  }

  escape() {
    if (this.state.selected.size) {
      this.state.selected.clear();
      updateRows(this);
      this.renderStatus();
    } else if (this.state.reader && !this.wide()) {
      this.closeReader();
    } else if (this.state.search) {
      this.closeSearch();
    }
  }

  toggleSelect() {
    const key = this.cursorKey();
    if (!key) return;
    if (this.state.selected.has(key)) this.state.selected.delete(key);
    else this.state.selected.add(key);
    updateRows(this);
    this.renderStatus();
    // As in most mail clients, the cursor then moves on to the next one.
    this.move(1);
  }

  // -------------------------------------------------------------------------
  // Actions
  // -------------------------------------------------------------------------

  /** The conversations acted on: the selection, the open one, or the cursor. */
  targets() {
    const { selected, threads, reader } = this.state;
    if (selected.size) return threads.filter((t) => selected.has(threadKey(t)));
    if (reader) {
      const t = threads.find((x) => threadKey(x) === reader.key);
      return [t || { account: reader.account, thread: reader.thread, latest: { role: reader.view }, unread: 0 }];
    }
    const t = this.cursorThread();
    return t ? [t] : [];
  }

  roleOf(t) {
    return this.state.search ? t.latest.role : this.state.view.role;
  }

  async act(action, { folder = '', list = this.targets() } = {}) {
    if (!list.length) return;
    const removed = MOVES.has(action) && list.filter((t) => {
      const role = this.roleOf(t);
      return !(action === 'done' && role === 'archive') && !(action === 'inbox' && role === 'inbox') && !(action === 'trash' && role === 'trash');
    });
    if (MOVES.has(action) && !removed.length) return;

    // What the server will do, shown at once.
    if (removed) {
      this.removeThreads(removed);
    } else {
      for (const t of list) {
        if (action === 'read' || action === 'unread') t.unread = action === 'read' ? 0 : Math.max(1, t.count);
        if (action === 'star' || action === 'unstar') t.flagged = action === 'star';
      }
      const r = this.state.reader;
      if (r && list.some((t) => threadKey(t) === r.key)) {
        for (const m of r.messages) {
          if (action === 'read' || action === 'unread') m.seen = action === 'read';
        }
        if (action === 'star' || action === 'unstar') {
          const latest = r.messages[0];
          // The messages may still be loading.
          if (action === 'unstar') for (const m of r.messages) m.flagged = false;
          else if (latest) latest.flagged = true;
        }
        renderReader(this);
      }
      updateRows(this);
      renderList(this);
    }

    // One request per folder (a search mixes folders).
    const byRole = new Map();
    for (const t of removed || list) {
      const role = this.roleOf(t);
      if (!byRole.has(role)) byRole.set(role, []);
      byRole.get(role).push({ account: t.account, thread: t.thread });
    }
    const n = (removed || list).length;
    const label = action === 'move' ? `Moved to ${folder}` : ACTION_LABELS[action];
    // On the stack at once, so that a u pressed before the server answers
    // undoes this action and not the one before. A deletion for good
    // cannot be undone.
    const ids = [];
    let failure = null;
    const request = (async () => {
      try {
        for (const [role, threads] of byRole) {
          const res = await api.act(action, role, { threads, folder });
          ids.push(...res.undo);
        }
      } catch (err) {
        failure = err;
      }
      return ids; // what was done, even when a later request failed
    })();
    const entry = action === 'delete' ? null : this.undos.push({
      label,
      undo: async () => {
        const done = await request;
        if (!done.length) return false; // the action itself failed
        try {
          await api.undo(done);
        } finally {
          this.refresh();
        }
        return true;
      },
    }, { quiet: true });
    await request;
    // Nothing was done: nothing to undo.
    if (entry && !ids.length) entry.cancel();
    if (failure) {
      toast(failure.message, { type: 'error' });
      this.refresh();
      return;
    }
    toast(n > 1 ? `${label} · ${n} conversations` : label, entry && ids.length
      ? { action: { label: 'Undo', run: () => this.undos.run() } } : {});
    this.renderStatus();
  }

  /** Takes conversations out of the list; the reader moves to the next one. */
  removeThreads(list) {
    const keys = new Set(list.map(threadKey));
    const { threads } = this.state;
    const wasReading = this.state.reader && keys.has(this.state.reader.key);
    const cursorKey = this.cursorKey();
    let next = this.state.cursor;
    if (keys.has(cursorKey)) {
      // The first conversation after the cursor that stays.
      while (next < threads.length && keys.has(threadKey(threads[next]))) next++;
      if (next >= threads.length) {
        next = this.state.cursor;
        while (next >= 0 && keys.has(threadKey(threads[next]))) next--;
      }
    }
    const nextThread = threads[next] && !keys.has(threadKey(threads[next])) ? threads[next] : null;
    this.state.threads = threads.filter((t) => !keys.has(threadKey(t)));
    for (const k of keys) this.state.selected.delete(k);
    this.state.cursor = nextThread ? this.state.threads.indexOf(nextThread) : 0;
    renderList(this);
    if (wasReading) {
      if (nextThread) this.openThread(nextThread);
      else this.closeReader();
    }
    this.follow();
    this.renderStatus();
  }

  toggleRead() {
    const list = this.targets();
    if (!list.length) return;
    this.act(list.some((t) => t.unread > 0) ? 'read' : 'unread', { list });
  }

  toggleStar() {
    const list = this.targets();
    if (!list.length) return;
    this.act(list.some((t) => t.flagged) ? 'unstar' : 'star', { list });
  }

  /** m: moves the conversations to a folder picked from the list. */
  async openMoveMenu(list = this.targets()) {
    if (!list.length) return;
    const accounts = new Set(list.map((t) => t.account));
    if (accounts.size > 1) {
      toast('Select conversations of one account to move them', { type: 'error' });
      return;
    }
    // The picker opens at once: what is typed next filters it.
    const account = [...accounts][0];
    let folder = await pickFolder(api.folders(account));
    if (folder?.create) folder = await this.createFolder(account, folder.create);
    if (folder) this.act('move', { folder, list });
  }

  /**
   * Conversations dragged from the list onto a folder of the sidebar: the
   * one grabbed, or the selection it belongs to. Not on a phone, where the
   * sidebar is a drawer and a finger swipes the row.
   */
  bindDrag() {
    let list = [];
    const grabbed = (row) => {
      const key = row.dataset.key;
      const { selected, threads } = this.state;
      if (selected.has(key)) return threads.filter((t) => selected.has(threadKey(t)));
      return threads.filter((t) => threadKey(t) === key);
    };
    draggable($('thread-list'), {
      rows: '.thread-row',
      accept: () => this.wide(),
      label: (row) => {
        list = grabbed(row);
        return list.length === 1 ? '1 conversation' : `${list.length} conversations`;
      },
      targets: () => {
        const accounts = new Set(list.map((t) => t.account));
        if (accounts.size !== 1) return [];
        const here = new Set(list.map((t) => this.roleOf(t)));
        return [...document.querySelectorAll('#folders .folder-row[data-role]')]
          .filter((el) => accounts.has(el.dataset.owner) && !here.has(el.dataset.role)
            && el.dataset.role !== 'drafts' && el.dataset.role !== 'sent')
          .map((el) => ({ el, key: el.dataset.role }));
      },
      onDropOn: ({ target }) => {
        const action = { inbox: 'inbox', archive: 'done', trash: 'trash' }[target.key];
        if (action) this.act(action, { list });
        else this.act('move', { folder: target.el.dataset.name, list });
      },
    });
  }

  // -------------------------------------------------------------------------
  // Folders
  // -------------------------------------------------------------------------

  /** Makes a folder on the server; resolves to its name, or null. */
  async createFolder(account, name, parent = '') {
    try {
      const { role } = await api.createFolder(account, name, parent);
      if (!this.isExpanded(account)) this.toggleExpanded(account);
      await this.loadAccounts();
      toast(`Folder ${folderLabel(role)} created`);
      return role.slice('folder:'.length);
    } catch (err) {
      toast(err.message, { type: 'error' });
      return null;
    }
  }

  async newFolder(account, parent = '') {
    const name = await promptDialog(parent ? `New folder in ${folderLabel(parent)}` : 'New folder', { placeholder: 'Name', okText: 'Create' });
    if (name) await this.createFolder(account, name, parent);
  }

  async renameFolder(account, role) {
    const name = await promptDialog('Rename folder', { value: folderLabel(role), okText: 'Rename' });
    if (!name || name === folderLabel(role)) return;
    try {
      const res = await api.renameFolder(account, role, name);
      const { view } = this.state;
      if (view.role === role) view.role = res.role;
      await this.loadAccounts();
      if (view.role === res.role) await this.loadThreads();
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  async deleteFolder(account, role) {
    const gmail = this.accountOf(account)?.gmail;
    const ok = await confirmDialog(`Delete ${folderLabel(role)}?`, gmail
      ? 'The label is removed; its messages stay in All Mail.'
      : 'The folder is removed from the server. Only an empty folder can be deleted.', { okText: 'Delete' });
    if (!ok) return;
    try {
      await api.deleteFolder(account, role);
      const viewing = this.state.view.role === role;
      await this.loadAccounts();
      if (viewing) await this.loadThreads();
      toast(`Folder ${folderLabel(role)} deleted`);
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  // -------------------------------------------------------------------------
  // Context menus (right click), in place of the browser's
  // -------------------------------------------------------------------------

  /** A conversation: its actions, on the whole selection when it is part of it. */
  threadMenu(e, t, index) {
    const { selected, threads } = this.state;
    const list = selected.has(threadKey(t)) ? threads.filter((x) => selected.has(threadKey(x))) : [t];
    const one = list.length === 1;
    const role = this.roleOf(t);
    const unread = list.some((x) => x.unread > 0);
    const starred = list.some((x) => x.flagged);
    openContextMenu(e, [
      one ? { label: 'Open', icon: 'mailOpen', hint: 'Enter', run: () => { this.setCursor(index); this.openCursor(); } } : null,
      one ? 'separator' : null,
      role === 'inbox'
        ? { label: 'Archive', icon: 'done', hint: 'A', run: () => this.act('done', { list }) }
        : { label: 'Move to inbox', icon: 'inbox', hint: 'I', run: () => this.act('inbox', { list }) },
      { label: 'Move to…', icon: 'folder', hint: 'm', run: () => this.openMoveMenu(list) },
      role === 'trash' ? null : { label: 'Trash', icon: 'trash', hint: 'd', danger: true, run: () => this.act('trash', { list }) },
      'separator',
      { label: unread ? 'Mark as read' : 'Mark as unread', icon: unread ? 'mailOpen' : 'mail', hint: 'U', run: () => this.act(unread ? 'read' : 'unread', { list }) },
      { label: starred ? 'Unstar' : 'Star', icon: 'star', hint: 's', run: () => this.act(starred ? 'unstar' : 'star', { list }) },
      { label: selected.has(threadKey(t)) ? 'Deselect' : 'Select', icon: 'check', hint: 'x', run: () => {
        const key = threadKey(t);
        if (selected.has(key)) selected.delete(key);
        else selected.add(key);
        updateRows(this);
        this.renderStatus();
      } },
    ], { title: one ? t.latest.subject || '(no subject)' : `${list.length} conversations` });
  }

  /** A folder of the sidebar. */
  folderMenu(e, account, folder) {
    const custom = isCustom(folder.role);
    openContextMenu(e, [
      { label: 'New folder', icon: 'plus', run: () => this.newFolder(account.id) },
      custom ? { label: 'New folder inside', icon: 'folder', run: () => this.newFolder(account.id, folder.role) } : null,
      custom ? 'separator' : null,
      custom ? { label: 'Rename', icon: 'edit', run: () => this.renameFolder(account.id, folder.role) } : null,
      custom ? { label: 'Delete', icon: 'trash', danger: true, run: () => this.deleteFolder(account.id, folder.role) } : null,
      'separator',
      { label: 'Synchronize', icon: 'refresh', run: () => api.sync(account.id) },
    ], { title: folderLabel(folder.role) });
  }

  /** An account's header in the sidebar. */
  accountMenu(e, account) {
    const open = this.isExpanded(account.id);
    openContextMenu(e, [
      { label: 'New folder', icon: 'plus', run: () => this.newFolder(account.id) },
      { label: open ? 'Show the inbox only' : 'Show every folder', icon: 'chevronRight', hint: '+', run: () => this.toggleExpanded(account.id) },
      { label: 'Synchronize', icon: 'refresh', run: () => api.sync(account.id) },
    ], { title: account.name });
  }

  /** The message replied to: the focused one, or the latest of the cursor. */
  replyTarget() {
    const r = this.state.reader;
    if (r?.messages.length) {
      // Replying to a message of one's own (sent) answers its conversation.
      return r.messages[r.focused] || r.messages[0];
    }
    return this.cursorThread()?.latest || null;
  }

  reply(mode) {
    const m = this.replyTarget();
    if (!m || m.draft) return;
    openCompose(this, api.compose(mode, m.id));
  }

  compose(draft = {}) {
    const { accounts, view } = this.state;
    if (!accounts.length) return;
    openCompose(this, { account: view.account || accounts[0].id, ...draft });
  }

  editDraft(id) {
    if (!id) {
      const m = this.focusedMessage() || this.cursorThread()?.latest;
      if (!m?.draft) return;
      id = m.id;
    }
    openCompose(this, api.compose('draft', id));
  }

  async deleteDraft(id) {
    try {
      await api.act('delete', 'drafts', { messages: [id] });
      if (this.state.view.role === 'drafts') this.loadThreads({ keep: true });
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  afterSend() {
    if (this.state.reader) setTimeout(() => this.refreshReader(), 1500);
    if (this.state.view.role === 'drafts') this.loadThreads({ keep: true });
  }

  /** i: shows the remote images of the focused message (always: for its sender). */
  async showImages(id, always = false) {
    const r = this.state.reader;
    if (!r) return;
    const c = id ? r.cards.get(id) : this.focusedCard();
    if (!c?.full?.html) return;
    if (always) {
      try {
        await api.allowImages(c.m.id);
        c.full.images_allowed = true;
        toast(`Images of ${c.m.from.address} will always show`, { type: 'success' });
      } catch (err) {
        toast(err.message, { type: 'error' });
        return;
      }
    }
    c.showImages = true;
    renderBody(this, c);
  }

  toggleTextMode(id) {
    const r = this.state.reader;
    if (!r) return;
    const c = id ? r.cards.get(id) : this.focusedCard();
    if (!c?.full?.html) return;
    c.textMode = !c.textMode;
    renderBody(this, c);
  }

  /** o: opens an attachment of the focused message. */
  openAttachment() {
    if (!this.state.reader) {
      this.openCursor();
      return;
    }
    const c = this.focusedCard();
    const parts = c?.full?.parts || [];
    if (!parts.length) {
      toast('No attachment');
      return;
    }
    if (parts.length === 1) {
      window.open(api.partURL(c.m.id, parts[0].index), '_blank', 'noopener');
      return;
    }
    openMenu(c.el.querySelector('.attachments') || c.el, parts.map((p) => ({
      label: p.filename, icon: 'paperclip', run: () => window.open(api.partURL(c.m.id, p.index), '_blank', 'noopener'),
    })), { title: 'Attachments' });
  }

  // -------------------------------------------------------------------------
  // Search
  // -------------------------------------------------------------------------

  /** / : the search page, on the last search. */
  openSearch() {
    if (topModal()) return;
    openSearchPage(this, { query: this.lastQuery });
  }

  /** Shows the results of a query in the list; open: a conversation to open. */
  async runSearch(query, { open = null } = {}) {
    query = query.trim();
    if (!query) return this.closeSearch();
    this.lastQuery = query;
    this.state.search = { query };
    this.state.cursor = 0;
    this.state.selected.clear();
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    await this.loadThreads();
    if (!open || this.state.search?.query !== query) return;
    const i = this.state.threads.findIndex((t) => threadKey(t) === threadKey(open));
    if (i < 0) return;
    this.setCursor(i);
    this.openCursor();
  }

  closeSearch() {
    if (!this.state.search) return;
    this.state.search = null;
    this.state.cursor = 0;
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    this.loadThreads();
  }

  /** Looks on the servers for older mail matching the words of a query. */
  async searchServer(query = this.state.search?.query) {
    if (!query) return;
    toast('Searching the servers…');
    try {
      const { found } = await api.searchServer(query);
      toast(found ? `${found} found on the servers` : 'Nothing more on the servers');
      if (this.state.search) await this.loadThreads({ keep: true });
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  // -------------------------------------------------------------------------
  // mailto: links
  // -------------------------------------------------------------------------

  /** #mailto=mailto:… (protocol handler) or #compose?to=… opens the composer. */
  handleHash() {
    if (!this.authenticated) return;
    const hash = decodeURIComponent(location.hash.slice(1));
    let url;
    if (hash.startsWith('mailto=')) url = hash.slice(7);
    else if (hash.startsWith('compose')) url = `mailto:${hash.slice(7).replace(/^\?/, '?')}`;
    else return;
    history.replaceState(null, '', location.pathname);
    const { to, cc, bcc, subject, body } = parseMailto(url);
    this.compose({ to, cc, bcc, subject, text: body });
  }

  // -------------------------------------------------------------------------
  // Help and settings
  // -------------------------------------------------------------------------

  openHelp() {
    if (topModal()?.el.classList.contains('help')) return topModal().close();
    openHelp('Keyboard shortcuts', this.keyTable());
  }

  async syncNow() {
    try {
      await api.sync();
      toast('Checking for new mail…');
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  registerMailto() {
    try {
      navigator.registerProtocolHandler('mailto', `${location.origin}/#mailto=%s`);
      toast('Your browser will ask to open mailto: links here');
    } catch (err) {
      toast(`Not supported by this browser: ${err.message}`, { type: 'error' });
    }
  }

  // -------------------------------------------------------------------------
  // Authentication
  // -------------------------------------------------------------------------

  showLogin() {
    closePopover();
    this.lockScreen.showLogin();
  }

  showFatal(err) {
    const retry = h('button', { type: 'button', class: 'button primary', onclick: () => { modal.close(); this.boot(); } }, 'Retry');
    const modal = openModal({
      title: 'Mail is unavailable',
      className: 'dialog',
      dismissible: false,
      body: h('p', { class: 'dialog-message' }, err.message),
      footer: retry,
    });
  }

  // -------------------------------------------------------------------------
  // Static chrome
  // -------------------------------------------------------------------------

  // Phones: a tab bar instead of a drawer: the inbox of each account (no
  // inbox mixing them), Folders, Search. The sidebar is the Folders page; a
  // folder is a page pushed from it (back: "‹ Folders" or a swipe from the
  // left edge).

  /** An inbox, of any account: one of the account tabs. */
  inInbox() {
    const { view, search } = this.state;
    return !search && view.role === 'inbox';
  }

  /** One tab per account inbox ("inbox:perso"), or "inbox" for a single account. */
  inboxTab(account) {
    return this.state.accounts.length > 1 ? `inbox:${account}` : 'inbox';
  }

  /** (Re)builds the tab bar when the accounts change. */
  buildTabs() {
    const { accounts } = this.state;
    const key = accounts.map((a) => `${a.id}:${a.name}`).join(',');
    if (this.tabs && key === this.tabsKey) return;
    this.tabsKey = key;
    const inboxes = accounts.length > 1
      ? accounts.map((a) => ({ id: this.inboxTab(a.id), label: a.name, icon: 'inbox' }))
      : [{ id: 'inbox', label: 'Inbox', icon: 'inbox' }];
    this.tabs = tabBar($('tabbar'), [
      ...inboxes,
      { id: 'folders', label: 'Folders', icon: 'folder' },
      { id: 'search', label: 'Search', icon: 'search' },
    ], { onSelect: (id) => this.selectTab(id) });
  }

  selectTab(id) {
    if (id === 'search') return this.openSearch();
    if (id === 'folders') return this.showFolders();
    const account = id.startsWith('inbox:') ? id.slice('inbox:'.length) : '';
    this.hideFolders();
    if (this.inInbox() && this.state.view.account === account) $('thread-list').scrollTo({ top: 0, behavior: 'smooth' });
    else this.setView('inbox', account);
  }

  showFolders() {
    this.closeReader();
    document.body.classList.add('show-folders');
    this.renderTabs();
  }

  hideFolders() {
    document.body.classList.remove('show-folders');
    this.renderTabs();
  }

  /** "‹ Folders" in a folder; "‹ Inbox" in search results. */
  goBack() {
    if (this.state.search) this.selectTab(this.inboxTab(this.state.view.account));
    else this.showFolders();
  }

  renderTabs() {
    if (!this.tabs) return;
    const { view, search, accounts } = this.state;
    const folders = document.body.classList.contains('show-folders');
    const inFolder = !this.inInbox();
    let active = this.inboxTab(view.account);
    if (folders || (inFolder && !search)) active = 'folders';
    else if (search) active = 'search';
    this.tabs.setActive(active);
    document.body.classList.toggle('in-folder', inFolder);
    const label = $('menu-toggle').querySelector('.back-label');
    if (label) label.textContent = search ? 'Inbox' : 'Folders';
    for (const a of accounts) this.tabs.setBadge(this.inboxTab(a.id), a.folders.find((f) => f.role === 'inbox')?.unread || 0);
  }

  // A folder chosen in the sidebar: on a phone, its page replaces the Folders page.
  closeSidebar() {
    this.hideFolders();
  }

  bindChrome() {
    const setIcon = (id, name) => $(id).prepend(icon(name));
    setIcon('settings-menu', 'more');
    // Phones only: back from a folder to the Folders page.
    $('menu-toggle').append(icon('chevronLeft'), h('span', { class: 'back-label' }, 'Folders'));
    setIcon('search-button', 'search');
    setIcon('refresh-button', 'refresh');
    setIcon('fab', 'compose');
    $('compose-button').append(icon('compose'), h('span', null, 'New message'), h('kbd', null, 'c'));

    $('menu-toggle').addEventListener('click', () => this.goBack());
    edgeSwipe($('list-pane'), () => {
      if (!this.wide() && !this.inInbox()) this.goBack();
    });
    $('compose-button').addEventListener('click', () => this.compose());
    $('fab').addEventListener('click', () => this.compose());
    $('search-button').addEventListener('click', () => this.openSearch());
    $('refresh-button').addEventListener('click', () => this.syncNow());
    pullToRefresh($('thread-list'), $('pull-hint'), () => this.syncNow());
    this.bindDrag();
    edgeSwipe($('reader'), () => {
      if (!this.wide()) this.closeReader();
    });
    $('status-help').addEventListener('click', () => this.openHelp());
    $('status-sync').addEventListener('click', () => {
      const failed = this.state.accounts.filter((a) => a.status.state === 'error');
      if (failed.length) toast(failed.map((a) => `${a.name}: ${a.status.error}`).join(' · '), { type: 'error' });
    });
    $('settings-menu').addEventListener('click', () => openMenu($('settings-menu'), [
      { label: 'Check for new mail', icon: 'refresh', run: () => this.syncNow() },
      { label: 'Keyboard shortcuts', icon: 'search', hint: '?', run: () => this.openHelp() },
      { label: 'Open mailto: links here', icon: 'link', run: () => this.registerMailto() },
      { label: 'addy.io aliases', icon: 'mail', run: () => openAddySettings(this) },
    ], { title: 'Settings' }));
    const lock = $('auth-menu');
    lock.append(icon('lock'));
    lock.addEventListener('click', () => this.lockScreen.menu(lock));
  }
}

function readJSON(text) {
  try {
    return JSON.parse(text) || [];
  } catch {
    return [];
  }
}

/** Reads a mailto: address (RFC 6068). */
export function parseMailto(url) {
  const out = { to: [], cc: [], bcc: [], subject: '', body: '' };
  const raw = String(url).replace(/^mailto:/i, '');
  const [path, query = ''] = raw.split('?');
  const list = (s) => decodeURIComponent(s || '').split(',').map((a) => a.trim()).filter(Boolean).map((address) => ({ address }));
  out.to = list(path);
  for (const pair of query.split('&')) {
    const [k, v = ''] = pair.split('=');
    const value = decodeURIComponent(v.replace(/\+/g, '%20'));
    switch (k.toLowerCase()) {
      case 'to': out.to.push(...list(v)); break;
      case 'cc': out.cc = list(v); break;
      case 'bcc': out.bcc = list(v); break;
      case 'subject': out.subject = value; break;
      case 'body': out.body = value; break;
    }
  }
  return out;
}

/**
 * A list of folders filtered by typing; resolves to the name, to
 * { create: name } for a folder to make, or null. loading resolves to the
 * folders.
 */
function pickFolder(loading) {
  return new Promise((resolve) => {
    let answer = null;
    let active = 0;
    let folders = [];
    let loaded = false;
    let shown = folders;
    const input = h('input', { class: 'picker-input', placeholder: 'Move to…', autocomplete: 'off', spellcheck: false });
    const list = h('div', { class: 'picker-list' });
    const render = () => {
      list.replaceChildren(...(shown.length ? shown.map((f, i) => h('button', {
        type: 'button', class: ['picker-item', i === active && 'active', f.create && 'create'],
        onclick: () => { answer = f; m.close(); },
      }, icon(f.create ? 'plus' : 'folder'), h('span', { class: 'picker-label' }, f.create ? `New folder “${f.create}”` : f))) : [h('div', { class: 'picker-empty' }, 'No folder matches.')]));
      list.querySelector('.active')?.scrollIntoView({ block: 'nearest' });
    };
    const filter = () => {
      const typed = input.value.trim();
      const q = typed.toLowerCase();
      shown = folders.filter((f) => f.toLowerCase().includes(q));
      // A name no folder has: offer to make it, last.
      if (typed && loaded && !folders.some((f) => f.toLowerCase() === q)) shown.push({ create: typed });
      active = 0;
      render();
    };
    input.addEventListener('input', filter);
    input.addEventListener('keydown', (e) => {
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault();
        active = Math.max(0, Math.min(shown.length - 1, active + (e.key === 'ArrowDown' ? 1 : -1)));
        render();
      } else if (e.key === 'Enter' && shown[active]) {
        e.preventDefault();
        answer = shown[active];
        m.close();
      }
    });
    const m = openModal({
      className: 'picker',
      body: [h('div', { class: 'picker-search' }, icon('folder'), input), list],
      onClose: () => resolve(answer),
    });
    list.replaceChildren(h('div', { class: 'picker-empty' }, 'Loading folders…'));
    input.focus();
    loading.then((list) => {
      folders = list;
      loaded = true;
      filter();
    }, (err) => {
      m.close();
      toast(err.message, { type: 'error' });
    });
  });
}

const app = new App();
WIDE.addEventListener('change', () => app.follow());
window.addEventListener('DOMContentLoaded', () => app.start());
