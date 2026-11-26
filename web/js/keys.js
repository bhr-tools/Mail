// Mail's key table, run by the kit (keys.js): Escape, dialogs and fields
// first, then these bindings. The ? help is built from it. The frame of a
// message forwards its keys to app.handleKey.

import { initKeys as kitKeys } from '/kit/js/keys.js';

export function initKeys(app) {
  const scroll = (dir) => {
    const reader = document.getElementById('reader');
    reader.scrollBy({ top: dir * reader.clientHeight / 2 });
  };
  const table = () => [
    // Pairs shown on one line of the help, run by their hidden halves.
    { keys: ['j', 'k'], label: 'Next / previous conversation (shown beside)', group: 'Move' },
    { keys: ['j', 'ArrowDown'], hidden: true, run: () => app.move(1) },
    { keys: ['k', 'ArrowUp'], hidden: true, run: () => app.move(-1) },
    { keys: ['gg'], label: 'First conversation', group: 'Move', run: () => app.moveTo(0) },
    { keys: ['G'], label: 'Last conversation', group: 'Move', run: () => app.moveTo(app.state.threads.length - 1) },
    { keys: ['C-d', 'C-u'], label: 'Scroll the conversation down / up', group: 'Move' },
    { keys: ['C-d'], hidden: true, run: () => scroll(1) },
    { keys: ['C-u'], hidden: true, run: () => scroll(-1) },

    { keys: ['/'], label: 'Search, with filters (account, folder…)', group: 'Search', run: () => app.openSearch() },
    { keys: ['↓', '↑'], label: 'Through the results (j k outside the fields)', group: 'Search' },
    { keys: ['Enter'], label: 'Open the conversation', group: 'Search' },
    { keys: ['C-Enter'], label: 'Show the results in the list', group: 'Search' },

    // As in aerc: folders, or the messages of an open conversation.
    { keys: ['J', 'K'], label: 'Next / previous folder', group: 'Folders' },
    { keys: ['J'], hidden: true, run: () => app.moveFolder(1) },
    { keys: ['K'], hidden: true, run: () => app.moveFolder(-1) },
    { keys: ['+'], label: 'Fold / unfold the account', group: 'Folders', run: () => app.toggleCurrentSection() },
    { digit: true, label: 'Each account', group: 'Folders', helpKeys: ['1–5'], run: (n) => n > 0 && app.switchAccount(n - 1) },

    { keys: ['A'], label: 'Done (archive)', group: 'Act', run: () => app.act('done') },
    { keys: ['d', '#', 'Delete'], label: 'Trash', group: 'Act', run: () => app.act('trash') },
    { keys: ['I'], label: 'Back to the inbox', group: 'Act', run: () => app.act('inbox') },
    { keys: ['U'], label: 'Read / unread', group: 'Act', run: () => app.toggleRead() },
    { keys: ['s'], label: 'Star', group: 'Act', run: () => app.toggleStar() },
    { keys: ['m'], label: 'Move to a folder (type a new name to create it)', group: 'Act', run: () => app.openMoveMenu() },
    { keys: [' ', 'x'], label: 'Select and go to the next', group: 'Act', run: () => app.toggleSelect() },
    { keys: ['u'], label: 'Undo', group: 'Act', run: () => app.undos.run() },

    { keys: ['c'], label: 'New message', group: 'Write and read', run: () => app.compose() },
    { keys: ['r', 'R', 'f'], label: 'Reply, reply all, forward', group: 'Write and read' },
    { keys: ['r'], hidden: true, run: () => app.reply('reply') },
    { keys: ['R'], hidden: true, run: () => app.reply('all') },
    { keys: ['f'], hidden: true, run: () => app.reply('forward') },
    { keys: ['e'], label: 'Finish a draft', group: 'Write and read', run: () => app.editDraft() },
    { keys: ['Enter'], label: 'Open the conversation', group: 'Write and read', hidden: true, run: () => app.enter() },
    { keys: ['i'], label: 'Show remote images', group: 'Write and read', run: () => app.showImages() },
    { keys: ['t'], label: 'Plain text / formatted', group: 'Write and read', run: () => app.toggleTextMode() },
    { keys: ['o'], label: 'Open an attachment', group: 'Write and read', run: () => app.openAttachment() },
    { keys: ['C-Enter'], label: 'Send', group: 'Write and read' },
    { keys: ['Escape'], label: 'Close (keeps a draft)', group: 'Write and read', helpKeys: ['Esc'] },
    { keys: ['Escape'], hidden: true, run: () => app.escape() },
  ];
  app.keyTable = table;
  const { handleKey } = kitKeys({ table, onMode: () => app.renderStatus() });
  app.handleKey = handleKey;
}
