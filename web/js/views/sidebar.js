// Sidebar: every inbox together, then each account. An account is folded
// to its inbox (and the folder being viewed); unfolded, it shows every
// folder, the person's own (Gmail labels) included.

import { h, icon } from '../dom.js';

export const ROLES = ['inbox', 'archive', 'sent', 'drafts', 'trash'];
export const ROLE_LABELS = { inbox: 'Inbox', archive: 'Archive', sent: 'Sent', drafts: 'Drafts', trash: 'Trash' };
export const ROLE_ICONS = { inbox: 'inbox', archive: 'done', sent: 'send', drafts: 'draft', trash: 'trash' };
const CUSTOM = 'folder:';

export const isCustom = (role) => role.startsWith(CUSTOM);

/** The name shown for a folder: "Robotics" for "Projects/Robotics". */
export function folderLabel(role) {
  if (!isCustom(role)) return ROLE_LABELS[role] || role;
  const name = role.slice(CUSTOM.length);
  return name.slice(name.lastIndexOf('/') + 1);
}

function depth(role) {
  return isCustom(role) ? role.slice(CUSTOM.length).split('/').length - 1 : 0;
}

function folderRow(app, { label, iconName, count, unread, active, run, hint, indent = 0 }) {
  return h('button', {
    type: 'button',
    class: ['folder-row', active && 'active', unread && 'has-unread'],
    title: hint || label,
    style: indent ? { '--indent': indent } : undefined,
    onclick: () => { run(); app.closeSidebar(); },
  }, icon(iconName), h('span', { class: 'folder-name' }, label), count ? h('span', { class: 'folder-count' }, count) : null);
}

/**
 * The folders in sidebar order, as { role, account } views: what Shift+J /
 * Shift+K walk through. Folded accounts give their inbox only (and the
 * folder being viewed).
 */
export function navigation(app) {
  const { accounts, view, search } = app.state;
  const several = accounts.length > 1;
  const out = [];
  if (several) out.push({ role: 'inbox', account: '', label: 'All inboxes', all: true });
  for (const a of accounts) {
    const account = several ? a.id : '';
    const open = app.isExpanded(a.id);
    const viewing = (f) => !search && view.role === f.role && view.account === account;
    for (const f of a.folders) {
      if (open || f.role === 'inbox' || viewing(f)) out.push({ role: f.role, account, folder: f, owner: a });
    }
  }
  return out;
}

export function renderSidebar(app) {
  const { accounts, view, search } = app.state;
  const several = accounts.length > 1;
  const nav = document.getElementById('folders');
  const items = navigation(app);
  const rows = [];
  let owner = null;
  for (const item of items) {
    const active = !search && view.role === item.role && view.account === item.account;
    if (item.all) {
      const unread = accounts.reduce((n, a) => n + (a.folders.find((f) => f.role === 'inbox')?.unread || 0), 0);
      rows.push(folderRow(app, {
        label: 'All inboxes', iconName: 'layers', count: unread || '', unread: unread > 0, active,
        hint: 'All inboxes (&)', run: () => app.setView('inbox', ''),
      }));
      continue;
    }
    if (item.owner !== owner) {
      owner = item.owner;
      rows.push(sectionHeader(app, owner));
    }
    const f = item.folder;
    const custom = isCustom(f.role);
    // Unread counts only where they mean something to do.
    const count = f.role === 'inbox' || custom ? f.unread : f.role === 'drafts' ? f.total : 0;
    rows.push(folderRow(app, {
      label: folderLabel(f.role),
      iconName: custom ? 'folder' : ROLE_ICONS[f.role],
      count: count || '',
      unread: (f.role === 'inbox' || custom) && f.unread > 0,
      active,
      indent: depth(f.role),
      hint: `${f.name}${several ? ` · ${owner.name}` : ''}`,
      run: () => app.setView(f.role, item.account),
    }));
  }
  nav.replaceChildren(...rows);
}

function sectionHeader(app, a) {
  const open = app.isExpanded(a.id);
  return h('button', {
    type: 'button',
    class: ['nav-section', `acc-${app.colorOf(a.id)}`, open && 'open'],
    title: open ? 'Fold: inbox only (+)' : 'Show every folder (+)',
    attrs: { 'aria-expanded': String(open) },
    onclick: () => app.toggleExpanded(a.id),
  },
  h('span', { class: 'account-dot' }),
  h('span', { class: 'nav-section-name' }, a.name),
  a.status.state === 'error' ? h('span', { class: 'sync-error', title: a.status.error }, '!') : null,
  icon('chevronRight', 'nav-chevron'));
}
