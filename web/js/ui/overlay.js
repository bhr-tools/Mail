// Floating layers: popovers (anchored menus that become bottom sheets on
// phones), a stack of modal dialogs, and toasts. Escape and outside clicks are
// handled here, for every layer, in one place.

import { h, icon, isMobile } from '../dom.js';

// ---------------------------------------------------------------------------
// Popover
// ---------------------------------------------------------------------------

let popover = null; // { el, backdrop, anchor, onClose }

/**
 * Shows `content` next to `anchor` (desktop) or as a bottom sheet (phone).
 * Only one popover is open at a time. Returns a function closing it.
 */
export function openPopover(anchor, content, { onClose, title, className = '' } = {}) {
  closePopover();
  const sheet = isMobile();
  const el = h('div', { class: ['popover', sheet && 'sheet', className], attrs: { role: 'dialog' } },
    sheet ? h('div', { class: 'sheet-handle' }) : null,
    sheet && title ? h('div', { class: 'sheet-title' }, title) : null,
    content);
  const backdrop = sheet ? h('div', { class: 'sheet-backdrop', onclick: () => closePopover() }) : null;
  if (backdrop) document.body.append(backdrop);
  document.body.append(el);
  popover = { el, backdrop, anchor, onClose };
  if (!sheet) positionPopover(el, anchor);
  anchor?.classList.add('is-open');
  return closePopover;
}

export function closePopover() {
  if (!popover) return false;
  const { el, backdrop, anchor, onClose } = popover;
  popover = null;
  el.remove();
  backdrop?.remove();
  anchor?.classList.remove('is-open');
  onClose?.();
  return true;
}

export function isPopoverOpen() {
  return popover !== null;
}

/** Places the popover under its anchor, flipped above or shifted left to stay on screen. */
function positionPopover(el, anchor) {
  const margin = 8;
  const a = anchor.getBoundingClientRect();
  el.style.minWidth = `${Math.max(a.width, 180)}px`;
  const p = el.getBoundingClientRect();
  let top = a.bottom + 4;
  if (top + p.height > window.innerHeight - margin && a.top - p.height - 4 > margin) top = a.top - p.height - 4;
  top = Math.max(margin, Math.min(top, window.innerHeight - p.height - margin));
  let left = a.left;
  if (left + p.width > window.innerWidth - margin) left = Math.max(margin, a.right - p.width);
  el.style.top = `${top}px`;
  el.style.left = `${left}px`;
  el.style.maxHeight = `${window.innerHeight - top - margin}px`;
}

// Close the popover on any press outside it (and outside its anchor, which
// toggles it itself).
document.addEventListener('pointerdown', (e) => {
  if (popover && !popover.el.contains(e.target) && !popover.anchor?.contains(e.target) && !popover.backdrop) {
    closePopover();
  }
}, true);
window.addEventListener('resize', () => {
  if (popover && !popover.backdrop) closePopover();
});

/**
 * A list of choices for a popover: items are
 * { label, icon?, hint?, selected?, danger?, className?, run }.
 */
export function menuList(items) {
  return h('div', { class: 'menu', attrs: { role: 'menu' } },
    items.map((item) => item === 'separator'
      ? h('div', { class: 'menu-separator' })
      : h('button', {
        type: 'button',
        class: ['menu-item', item.selected && 'selected', item.danger && 'danger', item.className],
        attrs: { role: 'menuitem' },
        onclick: () => { closePopover(); item.run(); },
      },
      item.icon ? icon(item.icon) : null,
      h('span', { class: 'menu-label' }, item.label),
      item.hint ? h('span', { class: 'menu-hint' }, item.hint) : null)));
}

/** Opens a menu of items under anchor. */
export function openMenu(anchor, items, options) {
  return openPopover(anchor, menuList(items), options);
}

// ---------------------------------------------------------------------------
// Modals
// ---------------------------------------------------------------------------

const modals = []; // stack, top last

/**
 * Opens a modal dialog.
 *   title     header text (omit for a custom header inside `body`)
 *   body      element(s)
 *   footer    element(s), typically buttons
 *   dismissible  false for dialogs that must not be closed (login)
 *   onClose   called once when the modal closes
 *   onEscape  overrides what Escape does (default: close)
 * Returns { el, close }.
 */
export function openModal({ title, body, footer, className = '', dismissible = true, autofocus = true, onClose, onEscape }) {
  const box = h('div', { class: ['modal', className], tabIndex: -1, attrs: { role: 'dialog', 'aria-modal': 'true' } },
    title ? h('div', { class: 'modal-header' },
      h('h2', { class: 'modal-title' }, title),
      dismissible ? h('button', { type: 'button', class: 'icon-button', title: 'Close (Esc)', onclick: () => handle.onEscape() }, icon('close')) : null)
      : null,
    h('div', { class: 'modal-body' }, body),
    footer ? h('div', { class: 'modal-footer' }, footer) : null);
  const overlay = h('div', { class: 'modal-overlay' }, box);
  overlay.addEventListener('mousedown', (e) => {
    if (e.target === overlay && dismissible) handle.onEscape();
  });

  let closed = false;
  const handle = {
    el: box,
    overlay,
    dismissible,
    onEscape: onEscape || (() => handle.close()),
    close() {
      if (closed) return;
      closed = true;
      closePopover();
      modals.splice(modals.indexOf(handle), 1);
      overlay.remove();
      document.body.classList.toggle('has-modal', modals.length > 0);
      onClose?.();
    },
  };
  modals.push(handle);
  document.body.append(overlay);
  document.body.classList.add('has-modal');
  // Focus right away: keys typed just after the shortcut that opened the
  // dialog must reach its field, not the page.
  if (autofocus) box.querySelector('[autofocus], input:not([type=hidden]):not([readonly]), textarea')?.focus();
  else box.focus({ preventScroll: true });
  requestAnimationFrame(() => overlay.classList.add('open'));
  return handle;
}

export function topModal() {
  return modals[modals.length - 1] || null;
}

export function closeAllModals({ keepUndismissible = true } = {}) {
  for (const m of [...modals].reverse()) {
    if (!keepUndismissible || m.dismissible) m.close();
  }
}

/**
 * Handles Escape for the topmost layer. Returns true when something was closed.
 */
export function escapeTopLayer() {
  if (closePopover()) return true;
  const m = topModal();
  if (m && m.dismissible) {
    m.onEscape();
    return true;
  }
  return false;
}

/** Resolves to true (confirm) or false (cancel). */
export function confirmDialog(title, message, { okText = 'Confirm', cancelText = 'Cancel', danger = true } = {}) {
  return new Promise((resolve) => {
    let answer = false;
    const ok = h('button', { type: 'button', class: ['button', danger ? 'danger' : 'primary'], onclick: () => { answer = true; m.close(); } }, okText);
    const m = openModal({
      title,
      className: 'dialog',
      body: h('p', { class: 'dialog-message' }, message),
      footer: [h('button', { type: 'button', class: 'button', onclick: () => m.close() }, cancelText), ok],
      onClose: () => resolve(answer),
    });
    m.el.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        ok.click();
      }
    });
    requestAnimationFrame(() => ok.focus());
  });
}

/** Resolves to the trimmed text entered, or null when cancelled or empty. */
export function promptDialog(title, { value = '', placeholder = '', okText = 'Save' } = {}) {
  return new Promise((resolve) => {
    let answer = null;
    const input = h('input', { type: 'text', class: 'input', value, placeholder, maxLength: 200, autocomplete: 'off' });
    const submit = () => {
      answer = input.value.trim() || null;
      m.close();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        submit();
      }
    });
    const m = openModal({
      title,
      className: 'dialog',
      body: input,
      footer: [
        h('button', { type: 'button', class: 'button', onclick: () => m.close() }, 'Cancel'),
        h('button', { type: 'button', class: 'button primary', onclick: submit }, okText),
      ],
      onClose: () => resolve(answer),
    });
    requestAnimationFrame(() => input.select());
  });
}

// ---------------------------------------------------------------------------
// Toast
// ---------------------------------------------------------------------------

let toastEl = null;
let toastTimer = 0;

/** Shows a short message. type: 'info' | 'success' | 'error'. action: { label, run }. */
export function toast(message, { type = 'info', action = null } = {}) {
  if (!toastEl) {
    toastEl = h('div', { class: 'toast', attrs: { role: 'status', 'aria-live': 'polite' } });
    document.body.append(toastEl);
  }
  toastEl.className = `toast ${type}`;
  toastEl.replaceChildren(h('span', { class: 'toast-message' }, message));
  if (action) {
    toastEl.append(h('button', {
      type: 'button',
      class: 'toast-action',
      onclick: () => { hideToast(); action.run(); },
    }, action.label));
  }
  requestAnimationFrame(() => toastEl.classList.add('open'));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, action ? 6000 : 3500);
}

function hideToast() {
  toastEl?.classList.remove('open');
}
