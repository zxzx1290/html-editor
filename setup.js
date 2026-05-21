const fs = require('fs');

const src = 'node_modules/monaco-editor/min/vs';
const dst = 'static/monaco/vs';
fs.rmSync(dst, { recursive: true, force: true });
fs.mkdirSync('static/monaco', { recursive: true });
fs.cpSync(src, dst, { recursive: true });
console.log(`Copied ${src} → ${dst}`);

// 只複製下拉選單實際使用的主題
const THEMES = [
  'Monokai', 'Dracula',
  'Nord', 'Cobalt2',
  'Solarized-dark', 'Solarized-light',
  'Tomorrow-Night', 'Tomorrow-Night-Eighties',
];
const themeSrc = 'node_modules/monaco-themes/themes';
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
console.log(`Copied xterm assets → ${xtermDst}`);
