// The addy.io setting: the domain of the aliases, and the accounts whose
// address is a verified addy.io mailbox. Those accounts get an Alias field in
// the composer.

import { api } from '../api.js';
import { h } from '../dom.js';
import { openModal, toast } from '/kit/js/overlay.js';

export function openAddySettings(app) {
  const current = app.state.addy || { domain: '', accounts: [] };
  const error = h('div', { class: 'form-error', hidden: true });
  const domain = h('input', {
    type: 'text', class: 'input', value: current.domain, placeholder: 'username.anonaddy.com',
    autocomplete: 'off', spellcheck: false, attrs: { 'aria-label': 'Alias domain' },
  });
  const boxes = app.state.accounts.map((a) => ({
    id: a.id,
    input: h('input', { type: 'checkbox', checked: current.accounts.includes(a.id) }),
    account: a,
  }));
  const submit = h('button', { type: 'submit', class: 'button primary' }, 'Save');
  const form = h('form', { class: 'auth-form addy-form' },
    h('p', { class: 'dialog-message' },
      'Send from an alias: in the composer, write to people by their usual address and choose the alias. The message goes through addy.io, which shows the alias instead of your address. These accounts never send from their own address.'),
    error,
    h('label', { class: 'field' }, h('span', { class: 'field-label' }, 'Alias domain'), domain),
    h('div', { class: 'field' },
      h('span', { class: 'field-label' }, 'Accounts verified on addy.io'),
      boxes.map((b) => h('label', { class: 'checkbox addy-account' }, b.input,
        h('span', null, b.account.name), h('span', { class: 'addy-address' }, b.account.address)))),
    h('div', { class: 'addy-actions' },
      h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
      submit));
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    error.hidden = true;
    submit.disabled = true;
    try {
      app.state.addy = await api.setAddy(domain.value, boxes.filter((b) => b.input.checked).map((b) => b.id));
      modal.close();
      toast('addy.io settings saved', { type: 'success' });
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
  const modal = openModal({ title: 'addy.io aliases', body: form, className: 'dialog' });
  return modal;
}

/** The alias domain when account sends through addy.io, or ''. */
export function addyDomain(app, account) {
  const s = app.state.addy;
  return s?.domain && s.accounts.includes(account) ? s.domain : '';
}
