// Vim-style keyboard control. One handler decides, in order:
//   1. Escape closes the topmost popover or dialog, or leaves a field;
//   2. while a dialog is open, other keys belong to it;
//   3. while typing in a field (INSERT mode), keys belong to the field;
//   4. otherwise keys are commands.
// Components that handle a key themselves call preventDefault(), and this
// handler then ignores it. The frame of a message forwards its keys here.

import { escapeTopLayer, topModal, isPopoverOpen } from './ui/overlay.js';
import { isTextInput } from './dom.js';

// The digit row of an AZERTY keyboard, and of a QWERTY one.
const ACCOUNT_KEYS = { '&': 0, 'é': 1, '"': 2, "'": 3, '(': 4, 1: 0, 2: 1, 3: 2, 4: 3, 5: 4 };

export function initKeys(app) {
  const updateMode = () => app.renderStatus();
  document.addEventListener('focusin', updateMode);
  document.addEventListener('focusout', () => setTimeout(updateMode, 0));

  let prefix = '';
  let prefixTimer = 0;

  app.handleKey = (e) => {
    if (e.defaultPrevented || e.isComposing) return;

    if (e.key === 'Escape') {
      if (escapeTopLayer()) {
        e.preventDefault();
        return;
      }
      if (isTextInput(document.activeElement)) {
        e.preventDefault();
        if (document.activeElement.id === 'search-input') app.closeSearch();
        else document.activeElement.blur();
        return;
      }
    }
    if (topModal() || isPopoverOpen() || isTextInput(e.target)) return;
    if (e.ctrlKey && !e.metaKey && !e.altKey && (e.key === 'd' || e.key === 'u')) {
      // Half a page down / up in the conversation, as in Vim.
      const reader = document.getElementById('reader');
      reader.scrollBy({ top: (e.key === 'd' ? 1 : -1) * reader.clientHeight / 2 });
      e.preventDefault();
      return;
    }
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    if (prefix === 'g') {
      prefix = '';
      clearTimeout(prefixTimer);
      if (e.key === 'g') {
        app.moveTo(0);
        e.preventDefault();
      }
      return;
    }
    if (e.key === 'g') {
      prefix = 'g';
      prefixTimer = setTimeout(() => { prefix = ''; }, 1000);
      e.preventDefault();
      return;
    }
    if (command(e.key)) e.preventDefault();
  };

  window.addEventListener('keydown', (e) => app.handleKey(e));

  function command(key) {
    if (key in ACCOUNT_KEYS) {
      app.switchAccount(ACCOUNT_KEYS[key]);
      return true;
    }
    switch (key) {
      case 'j': case 'ArrowDown': app.move(1); return true;
      case 'k': case 'ArrowUp': app.move(-1); return true;
      case 'G': app.moveTo(app.state.threads.length - 1); return true;
      // As in aerc: folders, or the messages of an open conversation.
      case 'J': app.moveFolder(1); return true;
      case 'K': app.moveFolder(-1); return true;
      case '+': app.toggleCurrentSection(); return true;
      case 'Enter': app.enter(); return true;
      case 'o': app.openAttachment(); return true;
      case 'd': app.act('done'); return true;
      case 'D': case '#': case 'Delete': app.act('trash'); return true;
      case 'I': app.act('inbox'); return true;
      case 'u': app.toggleRead(); return true;
      case 's': app.toggleStar(); return true;
      case 'm': app.openMoveMenu(); return true;
      case 'z': app.undoLast(); return true;
      case ' ': case 'x': app.toggleSelect(); return true;
      case 'r': app.reply('reply'); return true;
      case 'R': app.reply('all'); return true;
      case 'f': app.reply('forward'); return true;
      case 'c': app.compose(); return true;
      case 'e': app.editDraft(); return true;
      case 'i': app.showImages(); return true;
      case 't': app.toggleTextMode(); return true;
      case '/': app.openSearch(); return true;
      case '?': app.openHelp(); return true;
      case 'Escape': app.escape(); return true;
    }
    return false;
  }
}
