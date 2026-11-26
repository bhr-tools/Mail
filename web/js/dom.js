// DOM helpers: the kit's (h(), icon()…), with Mail's icons registered.

import { registerIcons } from '/kit/js/dom.js';
import { Icons } from './icons.js';

export { h, icon, clear, isMobile, isTextInput, autoGrow } from '/kit/js/dom.js';

registerIcons(Icons);
