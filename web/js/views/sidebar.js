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
  // Not under the pointer while an account is being dragged: after the drop.
  if (drag) {
    drag.stale = true;
    return;
  }
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
    title: `${open ? 'Fold: inbox only' : 'Show every folder'} (+) · drag to reorder`,
    attrs: { 'aria-expanded': String(open), 'data-account': a.id },
    onclick: () => {
      if (dropped) return;
      app.toggleExpanded(a.id);
    },
    onpointerdown: (e) => startDrag(app, e, a.id),
  },
  h('span', { class: 'account-dot' }),
  h('span', { class: 'nav-section-name' }, a.name),
  a.status.state === 'error' ? h('span', { class: 'sync-error', title: a.status.error }, '!') : null,
  icon('chevronRight', 'nav-chevron'));
}

// Reordering the accounts: an account header is dragged (mouse or finger)
// above or below the others. The rows of the sidebar stay where they are; a
// line shows where the account will go.

let drag = null;
let dropped = false; // swallows the click that ends a drag

function sections(nav) {
  // Each account's header and the rows under it, in order.
  const out = [];
  for (const row of nav.children) {
    if (row.dataset.account) out.push({ id: row.dataset.account, header: row, last: row });
    else if (out.length) out[out.length - 1].last = row;
  }
  return out;
}

function startDrag(app, e, id) {
  if (e.button !== 0 || app.state.accounts.length < 2) return;
  const nav = document.getElementById('folders');
  const header = e.currentTarget;
  const startY = e.clientY;
  let index = -1;
  let moving = false;
  // The sidebar holds still from the press: a redraw would replace the header.
  drag = { stale: false };

  const mark = () => {
    nav.querySelectorAll('.drop-before, .drop-after').forEach((el) => el.classList.remove('drop-before', 'drop-after'));
    const list = sections(nav);
    if (index < list.length) list[index].header.classList.add('drop-before');
    else list[list.length - 1].last.classList.add('drop-after');
  };
  const move = (ev) => {
    if (ev.pointerId !== e.pointerId) return;
    if (!moving) {
      if (Math.abs(ev.clientY - startY) < 6) return;
      moving = true;
      header.classList.add('dragging');
      nav.classList.add('reordering');
    }
    ev.preventDefault();
    // The new place: before the first account whose middle is below the pointer.
    const list = sections(nav);
    index = list.length;
    for (let i = 0; i < list.length; i++) {
      const top = list[i].header.getBoundingClientRect().top;
      const bottom = list[i].last.getBoundingClientRect().bottom;
      if (ev.clientY < (top + bottom) / 2) {
        index = i;
        break;
      }
    }
    mark();
  };
  const end = (ev) => {
    if (ev.pointerId !== e.pointerId) return;
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', end);
    window.removeEventListener('pointercancel', end);
    const stale = drag.stale;
    drag = null;
    if (!moving) {
      // After the click this press makes, which needs the header in place.
      if (stale) setTimeout(() => renderSidebar(app));
      return;
    }
    dropped = true;
    setTimeout(() => { dropped = false; });
    nav.classList.remove('reordering');
    const from = sections(nav).findIndex((s) => s.id === id);
    // The account leaves its place: the places after it move up by one.
    if (ev.type === 'pointerup' && index !== from && index !== from + 1) app.moveAccount(id, index > from ? index - 1 : index);
    else renderSidebar(app);
  };
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', end);
  window.addEventListener('pointercancel', end);
}
