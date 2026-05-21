const fs = require('fs');
const esbuild = require('esbuild');

const src = 'node_modules/monaco-editor/min/vs';
const dst = 'static/monaco/vs';
fs.rmSync(dst, { recursive: true, force: true });
fs.mkdirSync('static/monaco', { recursive: true });
fs.cpSync(src, dst, { recursive: true });
console.log(`Copied ${src} → ${dst}`);

// 下拉選單實際使用的主題（全部來自 tm-themes，即 VS Code 原版）
const THEMES = [
  'monokai', 'dracula',
  'nord',
  'solarized-dark', 'solarized-light',
  'tokyo-night', 'one-dark-pro', 'github-dark',
];
const themeSrc = 'node_modules/tm-themes/themes';
const themeDst = 'static/themes';
fs.rmSync(themeDst, { recursive: true, force: true });
fs.mkdirSync(themeDst, { recursive: true });
for (const name of THEMES) {
  fs.copyFileSync(`${themeSrc}/${name}.json`, `${themeDst}/${name}.json`);
}
console.log(`Copied ${THEMES.length} themes → ${themeDst}`);

const xtermDst = 'static/xterm';
fs.rmSync(xtermDst, { recursive: true, force: true });
fs.mkdirSync(xtermDst, { recursive: true });
fs.copyFileSync('node_modules/@xterm/xterm/lib/xterm.js', `${xtermDst}/xterm.js`);
fs.copyFileSync('node_modules/@xterm/xterm/css/xterm.css', `${xtermDst}/xterm.css`);
fs.copyFileSync('node_modules/@xterm/addon-fit/lib/addon-fit.js', `${xtermDst}/addon-fit.js`);
fs.copyFileSync('node_modules/@xterm/addon-unicode11/lib/addon-unicode11.js', `${xtermDst}/addon-unicode11.js`);
console.log(`Copied xterm assets → ${xtermDst}`);

// TextMate engine：bundle vscode-textmate + vscode-oniguruma 成 IIFE，
// 配合 grammar 檔與 WASM 一起部署到 static/textmate/
const tmDst = 'static/textmate';
fs.rmSync(tmDst, { recursive: true, force: true });
fs.mkdirSync(`${tmDst}/grammars`, { recursive: true });

esbuild.buildSync({
  entryPoints: ['textmate-entry.js'],
  bundle: true,
  format: 'iife',
  globalName: 'TM',
  outfile: `${tmDst}/textmate.js`,
  minify: true,
  platform: 'browser',
  target: ['es2020'],
});
console.log(`Bundled textmate engine → ${tmDst}/textmate.js`);

fs.copyFileSync('node_modules/vscode-oniguruma/release/onig.wasm', `${tmDst}/onig.wasm`);
console.log(`Copied onig.wasm → ${tmDst}/onig.wasm`);

const GRAMMARS = ['html', 'css', 'javascript', 'json', 'php'];
for (const name of GRAMMARS) {
  fs.copyFileSync(`node_modules/tm-grammars/grammars/${name}.json`, `${tmDst}/grammars/${name}.json`);
}
console.log(`Copied ${GRAMMARS.length} TextMate grammars → ${tmDst}/grammars`);
