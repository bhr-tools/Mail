// Mail web app: state, actions and orchestration of the views.

import { api, onUnauthorized } from './api.js';
import { h, icon } from './dom.js';
import { clock } from './format.js';
import { closeAllModals, closePopover, openMenu, openModal, toast, topModal } from './ui/overlay.js';
import { folderLabel, navigation, renderSidebar } from './views/sidebar.js';
import { renderList, threadKey, updateRows } from './views/list.js';
import { renderBody, renderReader, scrollToFocused } from './views/reader.js';
import { openCompose } from './views/compose.js';
import { initKeys } from './keys.js';

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
      auth: { no_auth: false, has_password: true, authenticated: false },
      accounts: [],
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
      undo: null, // { ids, label }
    };
    // Accounts showing every folder; the others show their inbox only.
    this.expanded = new Set(readJSON(prefs.get('expanded', '[]')));
    this.listSeq = 0;
    this.watching = false;
    this.handleKey = () => {};
  }

  async start() {
    this.bindChrome();
    initKeys(this);
    onUnauthorized(() => this.showLogin());
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible' && this.state.auth.authenticated) this.refresh();
    });
    window.addEventListener('hashchange', () => this.handleHash());
    renderReader(this);
    await this.boot();
  }

  async boot() {
    try {
      const status = await api.auth.status();
      this.state.auth = status;
      if (!status.no_auth && !status.has_password) return this.showSetup();
      if (!status.authenticated) return this.showLogin();
      await this.afterLogin();
    } catch (err) {
      this.showFatal(err);
    }
  }

  async afterLogin() {
    this.state.auth = { ...this.state.auth, has_password: true, authenticated: true };
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
    if (!this.state.version) this.state.version = data.version;
    const { view } = this.state;
    if (view.account && !this.accountOf(view.account)) view.account = '';
    // A folder gone from the server (a label deleted elsewhere).
    if (view.role.startsWith('folder:') && !this.state.accounts.some((a) => a.folders.some((f) => f.role === view.role))) {
      view.role = 'inbox';
    }
    if (this.state.accounts.length === 1) view.account = '';
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
    while (this.state.auth.authenticated) {
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
    return this.expanded.has(id);
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
      default: return 'No mail here.';
    }
  }

  // -------------------------------------------------------------------------
  // Rendering of the chrome
  // -------------------------------------------------------------------------

  renderTitle() {
    const { view, search, accounts } = this.state;
    const title = $('list-title');
    if (search) {
      title.replaceChildren('Search ', h('span', { class: 'muted' }, search.query));
    } else {
      const account = this.accountOf(view.account);
      const suffix = accounts.length > 1 ? (account ? account.name : 'All accounts') : '';
      title.replaceChildren(folderLabel(view.role), suffix ? h('span', { class: 'muted' }, ` · ${suffix}`) : '');
    }
    document.title = `${search ? 'Search' : folderLabel(view.role)} · Mail`;
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
    $('search-bar').hidden = true;
    this.state.cursor = 0;
    this.state.selected.clear();
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    this.loadThreads();
  }

  /** & é " ': every inbox, then each account, in the current folder. */
  switchAccount(index) {
    const { accounts } = this.state;
    if (accounts.length < 2) return;
    const account = index === 0 ? '' : accounts[index - 1]?.id;
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
    r.messages = data.messages;
    const last = r.messages.length - 1;
    // Every message unfolded; the reading starts at the first unread.
    for (const m of r.messages) r.expanded.add(m.id);
    const firstUnread = r.messages.findIndex((m) => !m.seen);
    r.focused = firstUnread >= 0 ? firstUnread : last;
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
      r.messages = data.messages;
      r.focused = Math.min(r.focused, r.messages.length - 1);
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
          const last = r.messages[r.messages.length - 1];
          // The messages may still be loading.
          if (action === 'unstar') for (const m of r.messages) m.flagged = false;
          else if (last) last.flagged = true;
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
    const ids = [];
    try {
      for (const [role, threads] of byRole) {
        const res = await api.act(action, role, { threads, folder });
        ids.push(...res.undo);
      }
    } catch (err) {
      toast(err.message, { type: 'error' });
      this.refresh();
      return;
    }
    const n = (removed || list).length;
    const label = action === 'move' ? `Moved to ${folder}` : ACTION_LABELS[action];
    this.state.undo = ids.length ? { ids, label } : null;
    toast(n > 1 ? `${label} · ${n} conversations` : label, ids.length && action !== 'delete'
      ? { action: { label: 'Undo (z)', run: () => this.undoLast() } } : {});
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

  async undoLast() {
    const undo = this.state.undo;
    if (!undo) {
      toast('Nothing to undo');
      return;
    }
    this.state.undo = null;
    try {
      await api.undo(undo.ids);
      toast(`Undone: ${undo.label.toLowerCase()}`);
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
    await this.refresh();
  }

  /** m: moves the conversations to a folder picked from the list. */
  async openMoveMenu() {
    const list = this.targets();
    if (!list.length) return;
    const accounts = new Set(list.map((t) => t.account));
    if (accounts.size > 1) {
      toast('Select conversations of one account to move them', { type: 'error' });
      return;
    }
    // The picker opens at once: what is typed next filters it.
    const folder = await pickFolder(api.folders([...accounts][0]));
    if (folder) this.act('move', { folder, list });
  }

  /** The message replied to: the focused one, or the latest of the cursor. */
  replyTarget() {
    const r = this.state.reader;
    if (r?.messages.length) {
      // Replying to a message of one's own (sent) answers its conversation.
      return r.messages[r.focused] || r.messages[r.messages.length - 1];
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

  openSearch() {
    const bar = $('search-bar');
    const input = $('search-input');
    bar.hidden = false;
    input.value = this.state.search?.query || '';
    input.focus();
    input.select();
  }

  runSearch(query) {
    query = query.trim();
    if (!query) return this.closeSearch();
    this.state.search = { query };
    this.state.cursor = 0;
    this.state.selected.clear();
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    $('search-input').blur();
    this.loadThreads();
  }

  closeSearch() {
    $('search-bar').hidden = true;
    $('search-input').blur();
    if (!this.state.search) return;
    this.state.search = null;
    this.state.cursor = 0;
    this.closeReader();
    renderSidebar(this);
    this.renderTitle();
    this.loadThreads();
  }

  async searchServer() {
    const query = this.state.search?.query;
    if (!query) return;
    toast('Searching the servers…');
    try {
      const { found } = await api.searchServer(query);
      toast(found ? `${found} found on the servers` : 'Nothing more on the servers');
      await this.loadThreads({ keep: true });
    } catch (err) {
      toast(err.message, { type: 'error' });
    }
  }

  // -------------------------------------------------------------------------
  // mailto: links
  // -------------------------------------------------------------------------

  /** #mailto=mailto:… (protocol handler) or #compose?to=… opens the composer. */
  handleHash() {
    if (!this.state.auth.authenticated) return;
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
    const row = (keys, label) => h('div', { class: 'help-row' }, h('span', null, label), h('span', { class: 'help-keys' }, keys.map((k) => h('kbd', null, k))));
    openModal({
      title: 'Keyboard shortcuts',
      className: 'help',
      body: h('div', { class: 'help-grid' },
        h('section', null,
          h('h3', { class: 'field-label' }, 'Move'),
          row(['j', 'k'], 'Next / previous conversation (shown beside)'),
          row(['g', 'g'], 'First conversation'),
          row(['G'], 'Last conversation'),
          row(['Ctrl', 'd'], 'Scroll the conversation down (Ctrl u: up)'),
          row(['/'], 'Search'),
          h('h3', { class: 'field-label' }, 'Folders'),
          row(['J', 'K'], 'Next / previous folder'),
          row(['+'], 'Fold / unfold the account'),
          row(['&', 'é', '"', "'"], 'All inboxes, then each account')),
        h('section', null,
          h('h3', { class: 'field-label' }, 'Act'),
          row(['d'], 'Done (archive)'),
          row(['D'], 'Trash'),
          row(['I'], 'Back to the inbox'),
          row(['u'], 'Read / unread'),
          row(['s'], 'Star'),
          row(['m'], 'Move to a folder'),
          row(['Space'], 'Select'),
          row(['z'], 'Undo'),
          h('h3', { class: 'field-label' }, 'Write and read'),
          row(['c'], 'New message'),
          row(['r', 'R', 'f'], 'Reply, reply all, forward'),
          row(['e'], 'Finish a draft'),
          row(['i'], 'Show remote images'),
          row(['t'], 'Plain text / formatted'),
          row(['o'], 'Open an attachment'),
          row(['Ctrl', 'Enter'], 'Send'),
          row(['Esc'], 'Close (keeps a draft)'))),
    });
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

  passwordForm({ title, intro, fields, submitLabel, dismissible, onSubmit }) {
    const error = h('div', { class: 'form-error', hidden: true });
    const inputs = fields.map((f) => h('input', {
      type: 'password', class: 'input', placeholder: f.placeholder, autocomplete: f.autocomplete, attrs: { 'aria-label': f.label },
    }));
    const submit = h('button', { type: 'submit', class: 'button primary wide' }, submitLabel);
    const form = h('form', { class: 'auth-form' },
      intro ? h('p', { class: 'dialog-message' }, intro) : null,
      error,
      fields.map((f, i) => h('label', { class: 'field' }, h('span', { class: 'field-label' }, f.label), inputs[i])),
      submit);
    let modal;
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      error.hidden = true;
      submit.disabled = true;
      try {
        const message = await onSubmit(inputs.map((i) => i.value));
        if (message) {
          error.textContent = message;
          error.hidden = false;
        } else {
          modal.close();
        }
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
      } finally {
        submit.disabled = false;
      }
    });
    modal = openModal({ title, body: form, className: 'dialog auth', dismissible });
    return modal;
  }

  showSetup() {
    closeAllModals({ keepUndismissible: false });
    this.state.auth.authenticated = false;
    this.passwordForm({
      title: 'Welcome to Mail',
      intro: 'Choose a master password to protect this instance. You can change it later.',
      fields: [
        { label: 'Master password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm password', placeholder: 'Repeat the password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Set password and continue',
      dismissible: false,
      onSubmit: async ([password, confirm]) => {
        if (password.length < 4) return 'The password must be at least 4 characters long.';
        if (password !== confirm) return 'The passwords do not match.';
        await api.auth.setup(password);
        this.afterLogin();
        return null;
      },
    });
  }

  showLogin() {
    if (topModal()?.el.classList.contains('auth')) return;
    closeAllModals({ keepUndismissible: false });
    closePopover();
    this.state.auth.authenticated = false;
    this.passwordForm({
      title: 'Mail is locked',
      intro: 'Enter your master password to unlock.',
      fields: [{ label: 'Password', placeholder: 'Master password', autocomplete: 'current-password' }],
      submitLabel: 'Unlock',
      dismissible: false,
      onSubmit: async ([password]) => {
        if (!password) return 'Enter your password.';
        await api.auth.login(password);
        this.afterLogin();
        return null;
      },
    });
  }

  changePassword() {
    this.passwordForm({
      title: 'Change password',
      fields: [
        { label: 'Current password', placeholder: 'Current master password', autocomplete: 'current-password' },
        { label: 'New password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm new password', placeholder: 'Repeat the new password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Update password',
      dismissible: true,
      onSubmit: async ([current, next, confirm]) => {
        if (next.length < 4) return 'The new password must be at least 4 characters long.';
        if (next !== confirm) return 'The new passwords do not match.';
        await api.auth.changePassword(current, next);
        toast('Password updated. Other devices were signed out.', { type: 'success' });
        return null;
      },
    });
  }

  async lock() {
    try {
      await api.auth.logout();
    } catch { /* locked anyway */ }
    this.showLogin();
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

  openSidebar() {
    $('sidebar').classList.add('open');
    $('sidebar-backdrop').classList.add('open');
    document.body.classList.add('sidebar-open');
  }

  closeSidebar() {
    $('sidebar').classList.remove('open');
    $('sidebar-backdrop').classList.remove('open');
    document.body.classList.remove('sidebar-open');
  }

  bindChrome() {
    const setIcon = (id, name) => $(id).prepend(icon(name));
    setIcon('settings-menu', 'more');
    setIcon('menu-toggle', 'menu');
    setIcon('search-button', 'search');
    setIcon('refresh-button', 'refresh');
    setIcon('fab', 'compose');
    $('compose-button').append(icon('compose'), h('span', null, 'New message'), h('kbd', null, 'c'));

    $('menu-toggle').addEventListener('click', () => this.openSidebar());
    $('sidebar-backdrop').addEventListener('click', () => this.closeSidebar());
    $('compose-button').addEventListener('click', () => this.compose());
    $('fab').addEventListener('click', () => this.compose());
    $('search-button').addEventListener('click', () => this.openSearch());
    $('refresh-button').addEventListener('click', () => this.syncNow());
    $('status-help').addEventListener('click', () => this.openHelp());
    $('status-sync').addEventListener('click', () => {
      const failed = this.state.accounts.filter((a) => a.status.state === 'error');
      if (failed.length) toast(failed.map((a) => `${a.name}: ${a.status.error}`).join(' · '), { type: 'error' });
    });
    $('search-input').addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        this.runSearch(e.target.value);
      }
    });
    $('settings-menu').addEventListener('click', () => openMenu($('settings-menu'), [
      { label: 'Check for new mail', icon: 'refresh', run: () => this.syncNow() },
      { label: 'Keyboard shortcuts', icon: 'search', hint: '?', run: () => this.openHelp() },
      { label: 'Open mailto: links here', icon: 'link', run: () => this.registerMailto() },
      ...(this.state.auth.no_auth ? [] : [
        'separator',
        { label: 'Change password', icon: 'key', run: () => this.changePassword() },
        { label: 'Lock', icon: 'lock', run: () => this.lock() },
      ]),
    ], { title: 'Settings' }));
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
 * A list of folders filtered by typing; resolves to the name, or null.
 * loading resolves to the folders.
 */
function pickFolder(loading) {
  return new Promise((resolve) => {
    let answer = null;
    let active = 0;
    let folders = [];
    let shown = folders;
    const input = h('input', { class: 'picker-input', placeholder: 'Move to…', autocomplete: 'off', spellcheck: false });
    const list = h('div', { class: 'picker-list' });
    const render = () => {
      list.replaceChildren(...(shown.length ? shown.map((f, i) => h('button', {
        type: 'button', class: ['picker-item', i === active && 'active'],
        onclick: () => { answer = f; m.close(); },
      }, icon('folder'), h('span', { class: 'picker-label' }, f))) : [h('div', { class: 'picker-empty' }, 'No folder matches.')]));
      list.querySelector('.active')?.scrollIntoView({ block: 'nearest' });
    };
    const filter = () => {
      const q = input.value.toLowerCase();
      shown = folders.filter((f) => f.toLowerCase().includes(q));
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
