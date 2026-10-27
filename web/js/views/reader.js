// The open conversation: its messages, newest last. Read messages are
// folded to one line; the HTML of a message is shown in a frame without
// scripts, its remote images blocked until asked for.

import { api } from '../api.js';
import { h, icon } from '../dom.js';
import { fileSize, formatAddresses, longDate, personName, renderText } from '../format.js';

/** Card of a message, kept between renders so frames are not reloaded. */
function card(app, r, m) {
  let c = r.cards.get(m.id);
  if (c) return c;
  c = { m, full: null, error: '', showImages: false, textMode: false, el: null, body: null, loading: false };
  const head = h('div', { class: 'message-head', onclick: () => app.toggleMessage(m.id) });
  c.body = h('div', { class: 'message-body' });
  c.el = h('article', { class: 'message-card', dataset: { id: m.id } }, head, c.body);
  c.head = head;
  r.cards.set(m.id, c);
  return c;
}

function renderHead(app, c) {
  const { m } = c;
  c.head.replaceChildren(
    h('div', { class: 'message-who' },
      h('div', { class: 'message-from' },
        h('strong', null, personName(m.from) || '(unknown sender)'),
        m.from.name ? h('span', { class: 'message-address' }, m.from.address) : null),
      h('div', { class: 'message-to', title: formatAddresses([...m.to, ...m.cc]) },
        m.to.length ? `To ${m.to.map(personName).join(', ')}` : '',
        m.cc.length ? ` · Cc ${m.cc.map(personName).join(', ')}` : ''),
      h('div', { class: 'message-snippet' }, m.snippet || '')),
    h('div', { class: 'message-meta' },
      m.draft ? h('span', { class: 'draft-label' }, 'Draft') : null,
      m.attachments ? icon('paperclip') : null,
      m.flagged ? icon('star') : null,
      h('span', { title: new Date(m.date).toLocaleString() }, longDate(m.date))));
}

/** Sizes a mail frame to its content, and keeps keys working inside it. */
function fitFrame(app, frame) {
  frame.addEventListener('load', () => {
    let doc;
    try {
      doc = frame.contentDocument;
    } catch {
      return;
    }
    if (!doc) return;
    const fit = () => {
      frame.style.height = `${Math.max(60, doc.documentElement.scrollHeight)}px`;
    };
    fit();
    try {
      new ResizeObserver(fit).observe(doc.body);
    } catch { /* fitted once */ }
    // Images arriving later change the height.
    for (const img of doc.images) img.addEventListener('load', fit);
    doc.addEventListener('keydown', (e) => app.handleKey(e));
  });
}

function imagesBanner(app, c) {
  const { full } = c;
  if (!full.html || c.textMode || !full.remote_images || full.images_allowed || c.showImages) return null;
  return h('div', { class: 'images-banner' },
    icon('image'),
    h('span', null, 'Remote images are hidden: they tell the sender when you read.'),
    h('span', { class: 'spacer' }),
    h('button', { type: 'button', class: 'button', onclick: () => app.showImages(c.m.id) }, 'Show images ', h('kbd', null, 'i')),
    h('button', {
      type: 'button', class: 'button ghost', title: `Always show the images of ${c.m.from.address}`,
      onclick: () => app.showImages(c.m.id, true),
    }, `Always for ${personName(c.m.from)}`));
}

function attachments(app, c) {
  const parts = c.full?.parts || [];
  if (!parts.length) return null;
  return h('div', { class: 'attachments' }, parts.map((p) => h('span', { class: 'attachment' },
    icon('paperclip'),
    h('a', { class: 'attachment-name', href: api.partURL(c.m.id, p.index), target: '_blank', rel: 'noopener', title: `Open ${p.filename}` }, p.filename),
    h('span', { class: 'attachment-size' }, fileSize(p.size)),
    h('a', { class: 'icon-button', href: api.partURL(c.m.id, p.index, true), download: p.filename, title: 'Download' }, icon('download')))));
}

/** (Re)builds the body of a card. */
export function renderBody(app, c) {
  if (c.error) {
    c.body.replaceChildren(h('div', { class: 'message-error' }, c.error));
    return;
  }
  if (!c.full) {
    c.body.replaceChildren(h('div', { class: 'message-loading' }, 'Loading…'));
    return;
  }
  const { full } = c;
  let content;
  if (full.html && !c.textMode) {
    content = h('iframe', {
      class: 'mail-frame',
      title: 'Message',
      src: api.htmlURL(c.m.id, c.showImages),
      attrs: { sandbox: 'allow-same-origin allow-popups allow-popups-to-escape-sandbox', referrerpolicy: 'no-referrer' },
    });
    fitFrame(app, content);
  } else {
    content = renderText(full.text);
  }
  c.body.replaceChildren(...[
    imagesBanner(app, c),
    content,
    attachments(app, c),
    c.m.draft
      ? h('div', { class: 'reply-bar', style: { padding: '12px 0 0' } },
        h('button', { type: 'button', class: 'button primary', onclick: () => app.editDraft(c.m.id) }, icon('edit'), 'Edit draft'))
      : null,
    full.html ? h('button', { type: 'button', class: 'body-switch', onclick: () => app.toggleTextMode(c.m.id) },
      c.textMode ? 'Show formatted message' : 'Show plain text', ' ', h('kbd', null, 't')) : null,
  ].filter(Boolean));
}

export async function loadBody(app, c) {
  if (c.full || c.loading) return;
  c.loading = true;
  renderBody(app, c);
  try {
    c.full = await api.message(c.m.id);
    c.error = '';
  } catch (err) {
    c.error = err.message;
  } finally {
    c.loading = false;
  }
  renderBody(app, c);
}

function actionButton(label, iconName, key, run, { on = false, optional = false } = {}) {
  return h('button', {
    type: 'button', class: ['icon-button', on && 'on', optional && 'optional'], title: `${label} (${key})`, attrs: { 'aria-label': label }, onclick: run,
  }, icon(iconName));
}

export function renderReader(app) {
  const el = document.getElementById('reader');
  const r = app.state.reader;
  if (!r) {
    el.replaceChildren(h('div', { class: 'reader-empty' },
      icon('mail'),
      h('p', null, 'No conversation open.'),
      h('p', null, h('kbd', null, 'j'), ' ', h('kbd', null, 'k'), ' to move, ', h('kbd', null, 'l'), ' to open.')));
    return;
  }
  const latest = r.messages[r.messages.length - 1];
  const starred = r.messages.some((m) => m.flagged);
  const unread = r.messages.some((m) => !m.seen);
  const role = r.view;
  const header = h('header', { class: 'reader-header' },
    h('button', { type: 'button', class: 'icon-button back-button', title: 'Back', onclick: () => app.closeReader() }, icon('arrowLeft')),
    h('h2', { class: 'reader-subject' }, r.subject || '(no subject)'),
    h('div', { class: 'reader-actions' },
      role === 'inbox' ? actionButton('Done', 'done', 'd', () => app.act('done')) : actionButton('Move to inbox', 'inbox', 'I', () => app.act('inbox')),
      role === 'trash' ? null : actionButton('Trash', 'trash', 'D', () => app.act('trash')),
      actionButton(unread ? 'Mark as read' : 'Mark as unread', unread ? 'mailOpen' : 'mail', 'u', () => app.act(unread ? 'read' : 'unread'), { optional: true }),
      actionButton(starred ? 'Unstar' : 'Star', 'star', 's', () => app.act(starred ? 'unstar' : 'star'), { on: starred }),
      actionButton('Move to…', 'folder', 'm', () => app.openMoveMenu(), { optional: true })));

  const list = h('div', { class: 'thread-messages' });
  r.messages.forEach((m, i) => {
    const c = card(app, r, m);
    c.m = m;
    renderHead(app, c);
    const expanded = r.expanded.has(m.id);
    c.el.classList.toggle('collapsed', !expanded);
    c.el.classList.toggle('focused', i === r.focused);
    if (expanded) {
      if (!c.full && !c.loading && !c.error) loadBody(app, c);
      else if (!c.body.firstChild) renderBody(app, c);
    }
    list.append(c.el);
  });

  const replyBar = latest && !latest.draft ? h('div', { class: 'reply-bar' },
    h('button', { type: 'button', class: 'button', onclick: () => app.reply('reply') }, icon('reply'), 'Reply ', h('kbd', null, 'r')),
    h('button', { type: 'button', class: 'button', onclick: () => app.reply('all') }, icon('replyAll'), 'Reply all ', h('kbd', null, 'R')),
    h('button', { type: 'button', class: 'button', onclick: () => app.reply('forward') }, icon('forward'), 'Forward ', h('kbd', null, 'f'))) : null;

  el.replaceChildren(header, list, replyBar || '');
}

/** Scrolls the focused message into view. */
export function scrollToFocused(app) {
  const r = app.state.reader;
  const m = r?.messages[r.focused];
  if (!m) return;
  r.cards.get(m.id)?.el.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
}
