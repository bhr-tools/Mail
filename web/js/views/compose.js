// The composer: a new message, a reply, a forward or a draft to finish.
// Plain text only. Escape keeps what was written as a draft.

import { api } from '../api.js';
import { h, icon } from '../dom.js';
import { fileSize, formatAddresses } from '../format.js';
import { confirmDialog, openModal, toast } from '../ui/overlay.js';

// "attached", "pièce jointe", "ci-joint"… in the new text (quotes left out).
const MENTIONS_ATTACHMENT = /(attach|enclosed|pi[eè]ce[s]? jointe|ci-joint|en pj\b|\bpj\b)/i;

function ownText(text) {
  return text.split('\n').filter((l) => !l.trimStart().startsWith('>')).join('\n');
}

/**
 * An address field completing from the address book: the last address of
 * the list is completed, ↑ ↓ choose, Tab or Enter accept.
 */
function addressField(label, value, { hidden = false } = {}) {
  const input = h('input', { class: 'compose-input', value, autocomplete: 'off', spellcheck: false, attrs: { 'aria-label': label } });
  const list = h('div', { class: 'suggestions', hidden: true });
  const row = h('label', { class: 'compose-field', hidden }, h('span', { class: 'compose-label' }, label), input, list);
  let items = [];
  let active = 0;
  let timer = 0;
  let seq = 0;

  const current = () => {
    const v = input.value;
    const start = v.lastIndexOf(',') + 1;
    return { start, word: v.slice(start).trim() };
  };
  const close = () => {
    list.hidden = true;
    items = [];
  };
  const accept = (i) => {
    const c = items[i];
    if (!c) return;
    const { start } = current();
    const before = input.value.slice(0, start).trimEnd();
    input.value = `${before ? `${before} ` : ''}${formatAddresses([c])}, `;
    close();
    input.focus();
  };
  const render = () => {
    list.replaceChildren(...items.map((c, i) => h('div', {
      class: ['suggestion', i === active && 'active'],
      onmousedown: (e) => { e.preventDefault(); accept(i); },
    }, h('span', null, c.name || c.address), c.name ? h('span', { class: 'suggestion-address' }, c.address) : null)));
    list.hidden = items.length === 0;
  };
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const { word } = current();
    if (word.length < 2) return close();
    timer = setTimeout(async () => {
      const mine = ++seq;
      const found = await api.contacts(word).catch(() => []);
      if (mine !== seq) return;
      items = found;
      active = 0;
      render();
    }, 120);
  });
  input.addEventListener('keydown', (e) => {
    if (list.hidden) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      active = (active + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      render();
    } else if (e.key === 'Enter' || e.key === 'Tab') {
      e.preventDefault();
      e.stopPropagation();
      accept(active);
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  });
  input.addEventListener('blur', () => setTimeout(close, 100));
  return { row, input };
}

function titleOf(draft) {
  if (draft.draft_id) return 'Draft';
  if (draft.in_reply_to) return 'Reply';
  if (draft.forward_of) return 'Forward';
  return 'New message';
}

/**
 * Opens the composer. source is a draft { account, to, cc, bcc, subject,
 * text, in_reply_to, forward_of, forwarded, draft_id }, or a promise of one:
 * the composer then opens at once, and what is typed before the draft
 * arrives is kept at the top of the message.
 */
export function openCompose(app, source = {}) {
  const accounts = app.state.accounts;
  const pending = typeof source.then === 'function';
  let draft = pending ? { account: app.state.view.account || accounts[0]?.id } : source;
  let ready = !pending;
  const from = h('select', { class: 'compose-input', attrs: { 'aria-label': 'From' } },
    accounts.map((a) => h('option', { value: a.id, selected: a.id === draft.account }, `${a.name} · ${a.address}`)));
  const to = addressField('To', formatAddresses(draft.to));
  const showCopies = (draft.cc?.length || 0) + (draft.bcc?.length || 0) > 0;
  const cc = addressField('Cc', formatAddresses(draft.cc), { hidden: !showCopies });
  const bcc = addressField('Bcc', formatAddresses(draft.bcc), { hidden: !showCopies });
  const copiesToggle = h('button', {
    type: 'button', class: 'compose-toggle', hidden: showCopies, title: 'Add Cc and Bcc',
    onclick: () => { cc.row.hidden = false; bcc.row.hidden = false; copiesToggle.hidden = true; cc.input.focus(); },
  }, 'Cc Bcc');
  to.row.append(copiesToggle);
  const subject = h('input', { class: 'compose-input', value: draft.subject || '', placeholder: 'Subject', attrs: { 'aria-label': 'Subject' } });
  const body = h('textarea', { class: 'compose-body', placeholder: 'Write your message…', attrs: { 'aria-label': 'Message' } });
  body.value = draft.text || '';

  // Attachments: parts of the original message (forward), and files.
  const forwarded = [...(draft.forwarded || [])];
  const files = [];
  const fileList = h('div', { class: 'compose-files' });
  const renderFiles = () => {
    fileList.replaceChildren(
      ...forwarded.map((p, i) => h('span', { class: 'attachment' }, icon('paperclip'),
        h('span', { class: 'attachment-name' }, p.filename), h('span', { class: 'attachment-size' }, fileSize(p.size)),
        h('button', { type: 'button', class: 'icon-button', title: 'Remove', onclick: () => { forwarded.splice(i, 1); renderFiles(); } }, icon('close')))),
      ...files.map((f, i) => h('span', { class: 'attachment' }, icon('paperclip'),
        h('span', { class: 'attachment-name' }, f.name), h('span', { class: 'attachment-size' }, fileSize(f.size)),
        h('button', { type: 'button', class: 'icon-button', title: 'Remove', onclick: () => { files.splice(i, 1); renderFiles(); } }, icon('close')))));
  };
  renderFiles();
  const picker = h('input', { type: 'file', multiple: true, hidden: true });
  picker.addEventListener('change', () => {
    files.push(...picker.files);
    picker.value = '';
    renderFiles();
  });

  const snapshot = () => JSON.stringify([to.input.value, cc.input.value, bcc.input.value, subject.value, body.value]);
  let initial = snapshot();
  const dirty = () => files.length > 0 || snapshot() !== initial;
  const message = () => ({
    account: from.value,
    to: to.input.value,
    cc: cc.input.value,
    bcc: bcc.input.value,
    subject: subject.value,
    text: body.value,
    in_reply_to: draft.in_reply_to || 0,
    forward_of: draft.forward_of || 0,
    forward_parts: forwarded.map((p) => p.index),
    draft_id: draft.draft_id || 0,
  });

  let busy = false;
  let modal;
  const status = h('span', { class: 'sending' });
  const send = async () => {
    if (busy || !ready) return;
    if (!to.input.value.trim() && !cc.input.value.trim() && !bcc.input.value.trim()) {
      toast('Add at least one recipient', { type: 'error' });
      to.input.focus();
      return;
    }
    if (!files.length && !forwarded.length && MENTIONS_ATTACHMENT.test(ownText(body.value))) {
      const ok = await confirmDialog('No attachment', 'The message mentions an attachment, but none is attached. Send it anyway?', { okText: 'Send', danger: false });
      if (!ok) return;
    }
    if (!subject.value.trim()) {
      const ok = await confirmDialog('No subject', 'Send this message without a subject?', { okText: 'Send', danger: false });
      if (!ok) return;
    }
    busy = true;
    status.textContent = 'Sending…';
    try {
      await api.send(message(), files);
      modal.close();
      toast('Message sent', { type: 'success' });
      app.afterSend();
    } catch (err) {
      status.textContent = '';
      toast(err.message, { type: 'error' });
    } finally {
      busy = false;
    }
  };
  const saveDraft = async ({ close = false } = {}) => {
    if (busy || !ready) return;
    busy = true;
    status.textContent = 'Saving…';
    try {
      await api.saveDraft(message(), files);
      toast('Draft saved', { type: 'success' });
      if (close) modal.close();
      else {
        status.textContent = 'Draft saved';
      }
    } catch (err) {
      status.textContent = '';
      toast(`Draft not saved: ${err.message}`, { type: 'error' });
    } finally {
      busy = false;
    }
  };
  const discard = async () => {
    if (dirty() && !(await confirmDialog('Discard this message?', 'What you wrote will be lost.', { okText: 'Discard' }))) return;
    if (draft.draft_id) await app.deleteDraft(draft.draft_id);
    modal.close();
  };

  modal = openModal({
    className: 'compose',
    title: pending ? 'Loading…' : titleOf(draft),
    body: [
      accounts.length > 1 ? h('label', { class: 'compose-field' }, h('span', { class: 'compose-label' }, 'From'), from) : null,
      to.row, cc.row, bcc.row,
      h('label', { class: 'compose-field' }, h('span', { class: 'compose-label' }, 'Subject'), subject),
      body,
      fileList,
      picker,
    ],
    footer: [
      h('button', { type: 'button', class: 'button ghost danger-text', title: 'Discard', onclick: discard }, icon('trash')),
      h('button', { type: 'button', class: 'button ghost', title: 'Attach files', onclick: () => picker.click() }, icon('paperclip'), 'Attach'),
      status,
      h('span', { class: 'spacer' }),
      h('button', { type: 'button', class: 'button', title: 'Save draft (Ctrl+S)', onclick: () => saveDraft({ close: true }) }, 'Save draft'),
      h('button', { type: 'button', class: 'button primary', title: 'Send (Ctrl+Enter)', onclick: send }, icon('send'), 'Send'),
    ],
    autofocus: false,
    // Escape keeps the message as a draft rather than losing it.
    onEscape: () => (ready && dirty() ? saveDraft({ close: true }) : modal.close()),
  });
  modal.el.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault();
      send();
    } else if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      saveDraft();
    }
  });
  // Files dropped on the composer are attached.
  modal.el.addEventListener('dragover', (e) => {
    if (![...e.dataTransfer.types].includes('Files')) return;
    e.preventDefault();
    modal.el.classList.add('dragging');
  });
  modal.el.addEventListener('dragleave', (e) => {
    if (!modal.el.contains(e.relatedTarget)) modal.el.classList.remove('dragging');
  });
  modal.el.addEventListener('drop', (e) => {
    e.preventDefault();
    modal.el.classList.remove('dragging');
    files.push(...e.dataTransfer.files);
    renderFiles();
  });

  // Replies start above the quote; new messages at the first empty field.
  const focus = () => {
    if (!to.input.value) to.input.focus();
    else if (!subject.value) subject.focus();
    else {
      body.focus();
      body.setSelectionRange(0, 0);
      body.scrollTop = 0;
    }
  };
  if (!pending) {
    focus();
    return modal;
  }

  body.focus();
  source.then((d) => {
    if (!modal.el.isConnected) return;
    draft = d;
    ready = true;
    const typed = body.value;
    from.value = d.account;
    to.input.value = formatAddresses(d.to);
    cc.input.value = formatAddresses(d.cc);
    bcc.input.value = formatAddresses(d.bcc);
    if (d.cc?.length || d.bcc?.length) {
      cc.row.hidden = false;
      bcc.row.hidden = false;
      copiesToggle.hidden = true;
    }
    subject.value = d.subject || '';
    body.value = d.text || '';
    initial = snapshot();
    forwarded.splice(0, forwarded.length, ...(d.forwarded || []));
    renderFiles();
    const title = modal.el.querySelector('.modal-title');
    if (title) title.textContent = titleOf(d);
    if (typed) {
      body.value = typed + body.value;
      body.focus();
      body.setSelectionRange(typed.length, typed.length);
    } else {
      focus();
    }
  }, (err) => {
    modal.close();
    toast(err.message, { type: 'error' });
  });
  return modal;
}
