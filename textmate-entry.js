// esbuild entry for bundling vscode-textmate + vscode-oniguruma as a single
// IIFE exposed on `window.TM`. Used by static/index.html.
import * as textmate from 'vscode-textmate';
import * as oniguruma from 'vscode-oniguruma';
export { textmate, oniguruma };
