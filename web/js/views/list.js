// The list of conversations of a folder or a search.

import { h, icon } from '../dom.js';
import { personName, shortDate } from '../format.js';

export const threadKey = (t) => `${t.account}\u0000${t.thread}`;

function people(app, t) {
  const { role } = app.state.view;
  // In sent mail and drafts, who it was written to matters.
  if (!app.state.search && (role === 'sent' || role === 'drafts')) {
    const to = t.latest.to.map(personName).join(', ');
    return to ? `To: ${to}` : '(no recipient)';
  }
  return t.people.length ? t.people.join(', ') : personName(t.latest.from);
}

function row(app, t, index) {
  const key = threadKey(t);
  const { cursor, selected, open } = app.state;
  const showAccount = app.state.accounts.length > 1 && (app.state.view.account === '' || app.state.search);
  const account = app.accountOf(t.account);
  return h('div', {
    class: ['thread-row', t.unread > 0 && 'unread', index === cursor && 'cursor', selected.has(key) && 'selected', open === key && 'open'],
    attrs: { role: 'option', 'aria-selected': String(index === cursor) },
    dataset: { index },
    onclick: (e) => {
      if (e.ctrlKey || e.metaKey) {
        app.setCursor(index);
        app.toggleSelect();
        return;
      }
      app.setCursor(index);
      app.openCursor();
    },
  },
  h('span', { class: 'unread-dot' }),
  h('div', { class: 'row-top' },
    h('span', { class: 'row-from' }, people(app, t)),
    t.count > 1 ? h('span', { class: 'row-count' }, t.count) : null),
  h('span', { class: 'row-date', title: new Date(t.date).toLocaleString() }, shortDate(t.date)),
  h('div', { class: 'row-subject' },
    showAccount && account ? h('span', { class: ['account-tag', `acc-${app.colorOf(t.account)}`] }, account.name) : null,
    h('span', { class: 'row-subject-text' }, t.latest.subject || '(no subject)'),
    h('span', { class: 'row-icons' },
      t.attachments ? icon('paperclip') : null,
      t.flagged ? icon('star') : null)),
  h('div', { class: 'row-snippet' }, t.latest.snippet || ' '));
}

export function renderList(app) {
  const el = document.getElementById('thread-list');
  const { threads, loading, more, search } = app.state;
  if (!threads.length) {
    el.replaceChildren(h('div', { class: 'list-empty' },
      loading ? 'Loading…' : search ? 'No conversation matches.' : app.emptyText()));
  } else {
    el.replaceChildren(...threads.map((t, i) => row(app, t, i)));
  }
  if (more) {
    el.append(h('button', { type: 'button', class: 'list-more', onclick: () => app.loadMore() }, 'Load more'));
  }
  if (search?.query) {
    el.append(h('button', { type: 'button', class: 'list-more', onclick: () => app.searchServer() },
      icon('search'), ' Search older mail on the servers'));
  }
  scrollToCursor(app);
}

export function scrollToCursor(app) {
  const el = document.querySelector(`.thread-row[data-index="${app.state.cursor}"]`);
  el?.scrollIntoView({ block: 'nearest' });
}

/** Updates the highlighted rows without rebuilding the list. */
export function updateRows(app) {
  const { cursor, selected, open, threads } = app.state;
  for (const el of document.querySelectorAll('.thread-row')) {
    const i = Number(el.dataset.index);
    const t = threads[i];
    if (!t) continue;
    const key = threadKey(t);
    el.classList.toggle('cursor', i === cursor);
    el.classList.toggle('selected', selected.has(key));
    el.classList.toggle('open', open === key);
    el.classList.toggle('unread', t.unread > 0);
    el.setAttribute('aria-selected', String(i === cursor));
  }
  scrollToCursor(app);
}
