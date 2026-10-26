// DOM helpers. Every element is built with h(): user data is always set as
// text, never parsed as HTML.

import { Icons } from './icons.js';

/**
 * Creates an element.
 *   h('button', { class: 'btn', onclick: fn, dataset: { id: 1 } }, 'Label', child)
 * Keys starting with "on" add event listeners; "class", "dataset", "style"
 * and "attrs" are handled specially; anything else is set as a property.
 */
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') el.className = Array.isArray(value) ? value.filter(Boolean).join(' ') : value;
    else if (key === 'dataset') Object.assign(el.dataset, value);
    else if (key === 'style') {
      // Custom properties (--name) need setProperty.
      for (const [prop, v] of Object.entries(value)) {
        if (prop.startsWith('--')) el.style.setProperty(prop, v);
        else el.style[prop] = v;
      }
    }
    else if (key === 'attrs') for (const [a, v] of Object.entries(value)) el.setAttribute(a, v);
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else el[key] = value;
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else el.append(child instanceof Node ? child : String(child));
  }
}

const iconCache = new Map();

/** Returns a fresh copy of a built-in SVG icon wrapped in a span. */
export function icon(name, className = '') {
  let tpl = iconCache.get(name);
  if (!tpl) {
    tpl = document.createElement('template');
    tpl.innerHTML = Icons[name] || ''; // trusted constant markup
    iconCache.set(name, tpl);
  }
  const span = document.createElement('span');
  span.className = `icon icon-${name} ${className}`.trim();
  span.append(tpl.content.cloneNode(true));
  return span;
}

export function clear(el) {
  el.replaceChildren();
  return el;
}

const mobileQuery = window.matchMedia('(max-width: 768px)');

/** True on phone-sized screens, where popovers become bottom sheets. */
export function isMobile() {
  return mobileQuery.matches;
}

export function isTextInput(el) {
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable);
}

/** Grows a textarea to fit its content. */
export function autoGrow(textarea, minHeight = 0) {
  textarea.style.height = 'auto';
  textarea.style.height = `${Math.max(minHeight, textarea.scrollHeight)}px`;
}

