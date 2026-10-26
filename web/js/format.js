// Formatting of dates, sizes, people and plain text messages.

import { h } from './dom.js';

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export const pad2 = (n) => String(n).padStart(2, '0');

function startOfDay(d) {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate());
}

/** Date of a list row: "09:31" today, "Mon" this week, "26 Oct" this year, else "26/10/2024". */
export function shortDate(iso, now = new Date()) {
  const d = new Date(iso);
  const days = Math.round((startOfDay(now) - startOfDay(d)) / 86_400_000);
  if (days <= 0) return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
  if (days < 7) return DAYS[d.getDay()];
  if (d.getFullYear() === now.getFullYear()) return `${d.getDate()} ${MONTHS[d.getMonth()]}`;
  return `${pad2(d.getDate())}/${pad2(d.getMonth() + 1)}/${d.getFullYear()}`;
}

/** Date of a message: "Mon 26 Oct, 09:31" (with the year if not this one). */
export function longDate(iso, now = new Date()) {
  const d = new Date(iso);
  const year = d.getFullYear() === now.getFullYear() ? '' : ` ${d.getFullYear()}`;
  return `${DAYS[d.getDay()]} ${d.getDate()} ${MONTHS[d.getMonth()]}${year}, ${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

export function clock(iso) {
  const d = new Date(iso);
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

export function fileSize(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/** "Alice", or the address without a name. */
export function personName(a) {
  return a?.name || a?.address || '';
}

/** "Alice <alice@x.org>", for address fields. */
export function formatAddress(a) {
  if (!a?.name) return a?.address || '';
  const name = /[",<>@]/.test(a.name) ? `"${a.name.replaceAll('"', '')}"` : a.name;
  return `${name} <${a.address}>`;
}

export function formatAddresses(list) {
  return (list || []).map(formatAddress).join(', ');
}

const URL_RE = /\b(https?:\/\/[^\s<>"]+[^\s<>".,;:!?)\]'])/g;

/** Appends text to el with its links clickable. */
function appendLinked(el, text) {
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    if (m.index > last) el.append(text.slice(last, m.index));
    el.append(h('a', { href: m[0], target: '_blank', rel: 'noopener noreferrer' }, m[0]));
    last = m.index + m[0].length;
  }
  if (last < text.length) el.append(text.slice(last));
}

/** Splits a message into its own text and the quoted part at the end. */
export function splitQuote(text) {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  // The quote starts at its intro line ("On … wrote:") or at the first line
  // of a final block of "> " lines.
  let start = lines.length;
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i].trim();
    if (l.startsWith('>') || l === '') start = i;
    else break;
  }
  if (start > 0 && /^(on .{4,120} wrote:|le .{4,120} a écrit\s?:)$/i.test(lines[start - 1].trim())) start -= 1;
  while (start < lines.length && lines[start].trim() === '') start++;
  return {
    own: lines.slice(0, start).join('\n').trimEnd(),
    quote: start < lines.length ? lines.slice(start).join('\n') : '',
  };
}

/** A plain text message: links clickable, quotes dimmed and folded. */
export function renderText(text) {
  const el = h('div', { class: 'mail-text' });
  const { own, quote } = splitQuote(text || '');
  appendLinked(el, own);
  if (quote) {
    const q = h('div', { class: 'quote', hidden: !!own });
    appendLinked(q, quote);
    if (own) {
      const toggle = h('button', { type: 'button', class: 'quote-toggle', title: 'Show the quoted text' }, '•••');
      toggle.addEventListener('click', () => { q.hidden = !q.hidden; });
      el.append('\n', toggle);
    }
    el.append(q);
  }
  return el;
}
