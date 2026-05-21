// esbuild entry for bundling vscode-textmate + vscode-oniguruma as a single
// IIFE exposed on `window.TM`. Used by static/index.html.
module.exports = {
    textmate: require('vscode-textmate'),
    oniguruma: require('vscode-oniguruma'),
};
