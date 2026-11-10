// Search: a full-screen page, as in the calendar. Results come as you type;
// the filters (account, folder, state, period, people) build the same query
// string a person could type ("from:camille in:archive newer:30d").

import { api } from '../api.js';
import { h, icon } from '../dom.js';
import { openModal, toast } from '../ui/overlay.js';
import { PERIODS, emptyForm, fromQuery, hasCriteria, toQuery } from '../search-query.js';
import { ROLE_LABELS, folderLabel, isCustom } from './sidebar.js';
import { rowContent, threadKey } from './list.js';

const PAGE = 50;
const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
const STANDARD = ['inbox', 'archive', 'sent', 'drafts', 'junk', 'trash'];

/** The account of a form: "account:" may name it by ID or by name. */
function accountId(app, value) {
  if (!value) return '';
  const v = value.toLowerCase();
  return app.state.accounts.find((a) => a.id.toLowerCase() === v || a.name.toLowerCase() === v)?.id ?? value;
}

const folderName = (role) => (isCustom(role) ? role.slice('folder:'.length) : ROLE_LABELS[role] || role);

/** A short reading of a query, for the title of the list: "rapport · INP · unread". */
export function describeQuery(app, query) {
  const form = fromQuery(query);
  const account = app.accountOf(accountId(app, form.account));
  return [
    form.text,
    form.from && `from ${form.from}`,
    form.to && `to ${form.to}`,
    account?.name || form.account,
    form.folder === 'all' ? 'anywhere' : form.folder && folderLabel(form.folder),
    form.unread && 'unread',
    form.starred && 'starred',
    form.attachment && 'with attachment',
    form.period && `last ${PERIODS.find(([p]) => p === form.period)?.[1]}`,
  ].filter(Boolean).join(' · ');
}

/** Opens the page on a query (the last search, to pick it up again). */
export function openSearchPage(app, { query = '' } = {}) {
  const form = fromQuery(query);
  form.account = accountId(app, form.account);
  const several = app.state.accounts.length > 1;

  const input = h('input', {
    class: 'picker-input', id: 'search-input', value: form.text, placeholder: 'Search mail',
    autocomplete: 'off', spellcheck: false, attrs: { 'aria-label': 'Search' },
  });
  const count = h('span', { class: 'search-count' });
  const accountChips = h('div', { class: 'search-chips' });
  const folderSelect = h('select', { class: 'input search-select', attrs: { 'aria-label': 'Folder' } });
  const stateChips = h('div', { class: 'search-chips' });
  const periodSwitch = h('div', { class: 'segmented' });
  const contacts = h('datalist', { id: 'search-contacts' });
  const personField = (key, label) => {
    const el = h('input', {
      class: 'input search-person', value: form[key], placeholder: label, autocomplete: 'off', spellcheck: false,
      attrs: { list: 'search-contacts', 'aria-label': label },
    });
    el.addEventListener('input', () => {
      form[key] = el.value;
      suggest(el.value);
      later();
    });
    return el;
  };
  const fromInput = personField('from', 'From');
  const toInput = personField('to', 'To');
  const list = h('div', { class: 'search-results', attrs: { role: 'listbox', 'aria-label': 'Results' } });

  let results = [];
  let more = false;
  let active = -1;
  let timer = 0;
  let seq = 0;
  let loadingMore = false;

  const currentQuery = () => toQuery(form);
  const later = () => {
    clearTimeout(timer);
    timer = setTimeout(search, 150);
  };

  // Account chips: one account or all of them.
  const drawAccounts = () => {
    if (!several) return;
    const chip = (id, label, color) => h('button', {
      type: 'button',
      class: ['search-chip', color && `acc-${color}`, form.account === id && 'on'],
      attrs: { 'aria-pressed': String(form.account === id) },
      onclick: () => {
        form.account = id;
        drawAccounts();
        drawFolders();
        search();
      },
    }, color ? h('span', { class: 'account-dot' }) : null, label);
    accountChips.replaceChildren(chip('', 'All accounts'),
      ...app.state.accounts.map((a) => chip(a.id, a.name, app.colorOf(a.id))));
  };

  // The usual folders, then those made by the person in the chosen account(s).
  const drawFolders = () => {
    const accounts = form.account ? app.state.accounts.filter((a) => a.id === form.account) : app.state.accounts;
    const custom = [...new Set(accounts.flatMap((a) => a.folders.map((f) => f.role)).filter(isCustom))]
      .sort((a, b) => a.localeCompare(b));
    if (isCustom(form.folder) && !custom.includes(form.folder)) custom.unshift(form.folder);
    const option = (value, label) => h('option', { value, selected: form.folder === value }, label);
    folderSelect.replaceChildren(
      option('', 'All folders'),
      ...STANDARD.map((r) => option(r, ROLE_LABELS[r])),
      ...(custom.length ? [h('optgroup', { label: 'Your folders' }, ...custom.map((r) => option(r, folderName(r))))] : []),
      option('all', 'Anywhere, with trash and spam'));
    folderSelect.value = form.folder;
  };
  folderSelect.addEventListener('change', () => {
    form.folder = folderSelect.value;
    search();
  });

  const drawStates = () => {
    const chip = (key, label, iconName) => h('button', {
      type: 'button',
      class: ['search-chip', form[key] && 'on'],
      attrs: { 'aria-pressed': String(form[key]) },
      onclick: () => {
        form[key] = !form[key];
        drawStates();
        search();
      },
    }, iconName ? icon(iconName) : null, label);
    stateChips.replaceChildren(chip('unread', 'Unread', 'mail'), chip('starred', 'Starred', 'star'), chip('attachment', 'Attachment', 'paperclip'));
  };

  const drawPeriods = () => periodSwitch.replaceChildren(...PERIODS.map(([value, label]) => h('button', {
    type: 'button',
    class: value === form.period && 'active',
    onclick: () => {
      form.period = value;
      drawPeriods();
      search();
    },
  }, label)));

  let suggestTimer = 0;
  const suggest = (word) => {
    clearTimeout(suggestTimer);
    if (word.trim().length < 2) return;
    suggestTimer = setTimeout(async () => {
      const found = await api.contacts(word.trim()).catch(() => []);
      contacts.replaceChildren(...found.map((c) => h('option', { value: c.address }, c.name || '')));
    }, 120);
  };

  const row = (t, i) => h('div', {
    class: ['thread-row', 'search-hit', t.unread > 0 && 'unread', i === active && 'cursor'],
    attrs: { role: 'option', 'aria-selected': String(i === active) },
    dataset: { index: i },
    onclick: () => go(t),
  }, ...rowContent(app, t, { showAccount: several && !form.account, inSearch: true }));

  const draw = () => {
    const asked = hasCriteria(form);
    count.textContent = asked ? `${results.length}${more ? '+' : ''} conversation${results.length === 1 ? '' : 's'}` : '';
    const items = [];
    if (!results.length) {
      items.push(h('div', { class: 'picker-empty' }, asked ? 'No conversation matches.' : 'Type to search subjects, people and text, or pick a filter.'));
    }
    // Grouped by month, the most recent first.
    let month = '';
    results.forEach((t, i) => {
      const d = new Date(t.date);
      const key = `${d.getFullYear()}-${d.getMonth()}`;
      if (key !== month) {
        month = key;
        items.push(h('div', { class: 'search-month' }, `${MONTHS[d.getMonth()]} ${d.getFullYear()}`));
      }
      items.push(row(t, i));
    });
    if (asked) {
      const actions = [];
      if (results.length) {
        actions.push(h('button', { type: 'button', class: 'list-more', onclick: () => showInList() },
          'Show in the list ', h('kbd', null, 'Ctrl'), h('kbd', null, '↵')));
      }
      if (fromQuery(currentQuery()).text.trim()) {
        actions.push(h('button', { type: 'button', class: 'list-more', onclick: () => searchServers() },
          icon('search'), ' Search older mail on the servers'));
      }
      items.push(h('div', { class: 'search-actions' }, ...actions));
    }
    list.replaceChildren(...items);
    list.querySelector('.search-hit.cursor')?.scrollIntoView({ block: 'nearest' });
  };

  const search = async () => {
    clearTimeout(timer);
    const id = ++seq;
    const q = currentQuery();
    app.lastQuery = q;
    if (!hasCriteria(form)) {
      results = [];
      more = false;
      active = -1;
      draw();
      return;
    }
    let found;
    try {
      found = await api.search(q, 0, PAGE);
    } catch (err) {
      if (id === seq) toast(err.message, { type: 'error' });
      return;
    }
    if (id !== seq) return;
    results = found;
    more = found.length === PAGE;
    active = -1;
    list.scrollTop = 0;
    draw();
  };

  const loadMore = async () => {
    if (!more || loadingMore) return;
    loadingMore = true;
    const id = seq;
    try {
      const found = await api.search(currentQuery(), results.length, PAGE);
      if (id !== seq) return;
      const keys = new Set(results.map(threadKey));
      results = results.concat(found.filter((t) => !keys.has(threadKey(t))));
      more = found.length === PAGE;
      draw();
    } catch (err) {
      toast(err.message, { type: 'error' });
    } finally {
      loadingMore = false;
    }
  };
  list.addEventListener('scroll', () => {
    if (list.scrollTop + list.clientHeight > list.scrollHeight - 300) loadMore();
  });

  const searchServers = async () => {
    await app.searchServer(currentQuery());
    search();
  };

  const go = (t) => {
    modal.close();
    app.runSearch(currentQuery(), { open: t });
  };

  const showInList = () => {
    if (!hasCriteria(form)) return;
    modal.close();
    app.runSearch(currentQuery());
  };

  const move = (step) => {
    if (!results.length) return;
    active = Math.min(results.length - 1, Math.max(0, active + step));
    if (active >= results.length - 5) loadMore();
    draw();
  };

  input.addEventListener('input', () => {
    form.text = input.value;
    later();
  });

  const modal = openModal({
    className: 'search-page',
    body: [
      h('div', { class: 'search-header' },
        h('button', { type: 'button', class: 'icon-button', title: 'Close (Esc)', onclick: () => modal.close() }, icon('chevronLeft')),
        h('div', { class: 'picker-search' }, icon('search'), input),
        count),
      h('div', { class: 'search-filters' },
        several ? accountChips : null,
        h('label', { class: 'search-field' }, icon('folder'), folderSelect),
        stateChips,
        periodSwitch,
        h('div', { class: 'search-people' }, fromInput, toInput, contacts)),
      list,
    ],
  });
  modal.el.addEventListener('keydown', (ev) => {
    const inField = ev.target.matches('input, select');
    const down = ev.key === 'ArrowDown' || (ev.ctrlKey && ev.key === 'j') || (!inField && ev.key === 'j');
    const up = ev.key === 'ArrowUp' || (ev.ctrlKey && ev.key === 'k') || (!inField && ev.key === 'k');
    if (ev.target === folderSelect && (ev.key === 'ArrowDown' || ev.key === 'ArrowUp')) return;
    if (down || up) {
      ev.preventDefault();
      move(down ? 1 : -1);
    } else if (ev.key === 'Enter' && (ev.ctrlKey || ev.metaKey)) {
      ev.preventDefault();
      showInList();
    } else if (ev.key === 'Enter' && ev.target.tagName !== 'BUTTON') {
      ev.preventDefault();
      if (results[active]) go(results[active]);
      else if (results.length === 1) go(results[0]);
      else if (inField) {
        // Enter in a field: on to the results, with j / k.
        clearTimeout(timer);
        search().then(() => {
          if (results.length) move(1);
        });
      }
    } else if (!inField && ev.key === '/') {
      ev.preventDefault();
      input.focus();
    }
  });

  drawAccounts();
  drawFolders();
  drawStates();
  drawPeriods();
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length);
  if (hasCriteria(form)) search();
  else draw();
  return modal;
}
