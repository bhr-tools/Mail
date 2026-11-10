// node --test tests/js/*.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { emptyForm, fromQuery, hasCriteria, toQuery } from '../../web/js/search-query.js';

test('a form makes the query the server reads', () => {
  const form = { ...emptyForm(), text: 'rapport', from: 'camille', account: 'inp', folder: 'folder:My receipts', unread: true, period: '30d' };
  assert.equal(toQuery(form), 'rapport from:camille account:inp in:"My receipts" is:unread newer:30d');
  assert.equal(toQuery({ ...emptyForm(), folder: 'archive', attachment: true, starred: true }), 'in:archive is:starred has:attachment');
});

test('a typed query fills the form back', () => {
  for (const form of [
    { ...emptyForm(), text: 'rapport', from: 'camille', account: 'inp', folder: 'folder:My receipts', unread: true, period: '30d' },
    { ...emptyForm(), text: '"lab report" stage', to: 'Léa Dubois', folder: 'all', starred: true, attachment: true, period: '7d' },
    { ...emptyForm(), folder: 'junk' },
  ]) {
    assert.deepEqual(fromQuery(toQuery(form)), form);
  }
  assert.equal(fromQuery('in:spam').folder, 'junk');
  assert.equal(fromQuery('in:done').folder, 'archive');
  assert.equal(fromQuery('in:Receipts').folder, 'folder:Receipts');
});

test('what is not a filter stays as words', () => {
  const form = fromQuery('newer:soon from: hello is:unread');
  assert.equal(form.text, 'newer:soon from: hello');
  assert.ok(form.unread);
});

test('a blank form asks for nothing', () => {
  assert.ok(!hasCriteria(emptyForm()));
  assert.ok(!hasCriteria({ ...emptyForm(), text: '  ' }));
  assert.ok(hasCriteria({ ...emptyForm(), unread: true }));
  assert.ok(hasCriteria({ ...emptyForm(), account: 'perso' }));
});
