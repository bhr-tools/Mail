// Sidebar: every inbox together, then the folders of each account.

import { h, icon } from '../dom.js';

export const ROLES = ['inbox', 'archive', 'sent', 'drafts', 'trash'];
export const ROLE_LABELS = { inbox: 'Inbox', archive: 'Archive', sent: 'Sent', drafts: 'Drafts', trash: 'Trash' };
export const ROLE_ICONS = { inbox: 'inbox', archive: 'done', sent: 'send', drafts: 'draft', trash: 'trash' };

function folderRow(app, { label, iconName, count, unread, active, run, hint }) {
  return h('button', {
    type: 'button',
    class: ['folder-row', active && 'active', unread && 'has-unread'],
    title: hint || label,
    onclick: () => { run(); app.closeSidebar(); },
  }, icon(iconName), h('span', { class: 'folder-name' }, label), count ? h('span', { class: 'folder-count' }, count) : null);
}

export function renderSidebar(app) {
  const { accounts, view, search } = app.state;
  const nav = document.getElementById('folders');
  const rows = [];
  const several = accounts.length > 1;
  if (several) {
    const unread = accounts.reduce((n, a) => n + (a.folders.find((f) => f.role === 'inbox')?.unread || 0), 0);
    rows.push(folderRow(app, {
      label: 'All inboxes', iconName: 'layers', count: unread || '', unread: unread > 0,
      active: !search && view.account === '' && view.role === 'inbox', hint: 'All inboxes (&)',
      run: () => app.setView('inbox', ''),
    }));
  }
  accounts.forEach((a, i) => {
    rows.push(h('div', { class: ['nav-section', `acc-${app.colorOf(a.id)}`] },
      h('span', { class: 'account-dot' }), h('span', null, a.name),
      a.status.state === 'error' ? h('span', { class: 'sync-error', title: a.status.error }, '!') : null));
    for (const role of ROLES) {
      const f = a.folders.find((x) => x.role === role);
      if (!f) continue;
      // Unread counts only where they mean something to do.
      const count = role === 'inbox' ? f.unread : role === 'drafts' ? f.total : 0;
      rows.push(folderRow(app, {
        label: ROLE_LABELS[role], iconName: ROLE_ICONS[role], count: count || '', unread: role === 'inbox' && f.unread > 0,
        active: !search && view.role === role && (view.account === a.id || (!several && view.account === '')),
        hint: `${ROLE_LABELS[role]} · ${f.name}${role === 'inbox' && i < 3 ? ` (${['é', '"', "'"][i]})` : ''}`,
        run: () => app.setView(role, several ? a.id : ''),
      }));
    }
  });
  nav.replaceChildren(...rows);
}
