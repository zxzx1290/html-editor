// esbuild entry for bundling iconv-lite-umd + jschardet as a single
// IIFE exposed on `window.Encoding`. Used by static/index.html.
import * as iconv from '@vscode/iconv-lite-umd';
import * as jschardet from 'jschardet';
export { iconv, jschardet };
