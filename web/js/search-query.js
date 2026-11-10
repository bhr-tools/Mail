// The search form and the query string the server reads (internal/api/query.go).
// The form builds the string; a typed string fills the form back, so filters
// typed by hand ("from:camille is:unread") show as set in the form.

export const PERIODS = [['', 'Any time'], ['7d', '7 days'], ['30d', '30 days'], ['1y', '1 year']];

// "in:" values: the folders every account has.
const ROLES = ['inbox', 'archive', 'sent', 'drafts', 'trash', 'junk'];
const ROLE_ALIASES = { done: 'archive', archived: 'archive', spam: 'junk', anywhere: 'all' };
const CUSTOM = 'folder:';

/**
 * A blank form. folder: '' searches everywhere but the trash and spam, 'all'
 * everywhere, a role one folder ('inbox', 'folder:Receipts').
 */
export function emptyForm() {
  return { text: '', account: '', folder: '', unread: false, starred: false, attachment: false, period: '', from: '', to: '' };
}

/** Whether the form asks for anything (a blank form lists nothing). */
export function hasCriteria(form) {
  return Object.entries(emptyForm()).some(([k, blank]) => {
    const v = form[k];
    return typeof v === 'string' ? v.trim() !== '' : v !== blank;
  });
}

const quote = (v) => (/\s/.test(v) ? `"${v.replace(/"/g, '')}"` : v);

/** The query string of a form. */
export function toQuery(form) {
  const parts = [];
  if (form.text.trim()) parts.push(form.text.trim());
  if (form.from.trim()) parts.push(`from:${quote(form.from.trim())}`);
  if (form.to.trim()) parts.push(`to:${quote(form.to.trim())}`);
  if (form.account) parts.push(`account:${quote(form.account)}`);
  if (form.folder) {
    const name = form.folder.startsWith(CUSTOM) ? form.folder.slice(CUSTOM.length) : form.folder;
    parts.push(`in:${quote(name)}`);
  }
  if (form.unread) parts.push('is:unread');
  if (form.starred) parts.push('is:starred');
  if (form.attachment) parts.push('has:attachment');
  if (form.period) parts.push(`newer:${form.period}`);
  return parts.join(' ');
}

/** Splits on spaces, keeping "quoted phrases" whole (as the server does). */
function tokenize(s) {
  const out = [];
  let cur = '';
  let quoted = false;
  let hadQuote = false;
  const flush = () => {
    const value = cur.trim();
    if (value) out.push({ value, quoted: hadQuote });
    cur = '';
    hadQuote = false;
  };
  for (const ch of s) {
    if (ch === '"') {
      quoted = !quoted;
      hadQuote = true;
    } else if ((ch === ' ' || ch === '\t') && !quoted) {
      flush();
    } else {
      cur += ch;
    }
  }
  flush();
  return out;
}

/** The form of a query string; what isn't a filter stays as the words. */
export function fromQuery(query) {
  const form = emptyForm();
  const words = [];
  for (const { value, quoted } of tokenize(query || '')) {
    const i = value.indexOf(':');
    const key = i > 0 ? value.slice(0, i).toLowerCase() : '';
    const v = i > 0 ? value.slice(i + 1) : '';
    let used = !!v;
    switch (v && key) {
      case 'from': form.from = v; break;
      case 'to': form.to = v; break;
      case 'account': form.account = v; break;
      case 'in': {
        const low = ROLE_ALIASES[v.toLowerCase()] || v.toLowerCase();
        form.folder = low === 'all' || ROLES.includes(low) ? low : CUSTOM + v;
        break;
      }
      case 'is':
        if (/^unread$/i.test(v)) form.unread = true;
        else if (/^(starred|flagged)$/i.test(v)) form.starred = true;
        break;
      case 'has':
        if (/^attachment/i.test(v)) form.attachment = true;
        break;
      case 'newer':
        if (PERIODS.some(([p]) => p && p === v.toLowerCase())) form.period = v.toLowerCase();
        else used = false;
        break;
      default:
        used = false;
    }
    if (!used) words.push(quoted && /\s/.test(value) ? `"${value}"` : value);
  }
  form.text = words.join(' ');
  return form;
}
