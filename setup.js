const fs = require('fs');
const esbuild = require('esbuild');

// Vue 3 的 global build（掛成 window.Vue，供 index.html 以 <script> 直接引用）。
// 用 dev 版保留 runtime 警告；正式部署想瘦身可改 dist/vue.global.prod.js。
fs.copyFileSync('node_modules/vue/dist/vue.global.js', 'static/vue.global.js');
console.log('Copied vue → static/vue.global.js');


// Monaco Editor 的資源檔（包含核心程式碼、語言定義、編輯器主題等）。
const src = 'node_modules/monaco-editor/min/vs';
const dst = 'static/monaco/vs';
fs.rmSync(dst, { recursive: true, force: true });
fs.mkdirSync('static/monaco', { recursive: true });
fs.cpSync(src, dst, { recursive: true });
console.log(`Copied ${src} → ${dst}`);


// 下拉選單實際使用的主題（全部來自 tm-themes，即 VS Code 原版）。
// dark-plus / light-plus = VS Code Dark+ / Light+，是預設選項。
const THEMES = [
  'dark-plus', 'light-plus',
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


// xterm.js 的核心程式碼與 CSS，還有 fit addon（讓 terminal 自動適應容器大小）和 unicode11 addon（提供更完整的 Unicode 支援）。
const xtermDst = 'static/xterm';
fs.rmSync(xtermDst, { recursive: true, force: true });
fs.mkdirSync(xtermDst, { recursive: true });
fs.copyFileSync('node_modules/@xterm/xterm/lib/xterm.js', `${xtermDst}/xterm.js`);
fs.copyFileSync('node_modules/@xterm/xterm/css/xterm.css', `${xtermDst}/xterm.css`);
fs.copyFileSync('node_modules/@xterm/addon-fit/lib/addon-fit.js', `${xtermDst}/addon-fit.js`);
fs.copyFileSync('node_modules/@xterm/addon-unicode11/lib/addon-unicode11.js', `${xtermDst}/addon-unicode11.js`);
fs.copyFileSync('node_modules/@xterm/addon-webgl/lib/addon-webgl.js', `${xtermDst}/addon-webgl.js`);
console.log(`Copied xterm assets → ${xtermDst}`);


// 編碼支援：把 iconv-lite-umd（編解碼）+ jschardet（偵測）bundle 成 IIFE，
// 掛成全域 window.Encoding（與 textmate 同模式，避免 UMD 污染全域 / 撞 AMD loader）。
const encDst = 'static/encoding';
fs.rmSync(encDst, { recursive: true, force: true });
fs.mkdirSync(encDst, { recursive: true });
esbuild.buildSync({
  entryPoints: ['encoding-entry.js'],
  bundle: true,
  format: 'iife',
  globalName: 'Encoding',
  outfile: `${encDst}/encoding.js`,
  minify: true,
  platform: 'browser',
  target: ['es2020'],
});
console.log(`Bundled encoding libs → ${encDst}/encoding.js`);


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


// vscode-oniguruma 的 WASM 二進位檔，TextMate engine 會載入它來解析 grammar。
fs.copyFileSync('node_modules/vscode-oniguruma/release/onig.wasm', `${tmDst}/onig.wasm`);
console.log(`Copied onig.wasm → ${tmDst}/onig.wasm`);


// Lucide icon font：複製 woff2 並產生精簡版 CSS（只引用 woff2，剔除 eot/ttf/woff/svg）。
const lucideDst = 'static/lucide';
fs.rmSync(lucideDst, { recursive: true, force: true });
fs.mkdirSync(lucideDst, { recursive: true });
fs.copyFileSync('node_modules/lucide-static/font/lucide.woff2', `${lucideDst}/lucide.woff2`);
const lucideCssRaw = fs.readFileSync('node_modules/lucide-static/font/lucide.css', 'utf8');
const lucideCssTrim = lucideCssRaw.replace(/@font-face\s*\{[\s\S]*?\}/, [
  '@font-face {',
  '  font-family: "lucide";',
  '  src: url("lucide.woff2") format("woff2");',
  '  font-display: swap;',
  '}',
].join('\n'));
fs.writeFileSync(`${lucideDst}/lucide.css`, lucideCssTrim);
console.log(`Copied lucide font → ${lucideDst}`);


// tm-grammars 的 php.json 是 source.php（純 PHP，僅給 <?php ... ?> 內部用），
// 缺了 .php 檔需要的入口 grammar text.html.php（HTML 為主、嵌入 source.php）。
// 那份 grammar 從 vscode 官方 PHP 套件 vendored 到 php-html.tmLanguage.json，
// 它會再 include text.html.derivative，所以一併複製 html-derivative.json。
const GRAMMARS = [
  'html', 'html-derivative', 'css', 'javascript', 'json', 'php',
  'python', 'go', 'rust', 'ruby', 'shellscript', 'markdown',
  'cpp', 'java', 'docker', 'yaml', 'sql', 'typescript',
  'vue',
];
for (const name of GRAMMARS) {
  fs.copyFileSync(`node_modules/tm-grammars/grammars/${name}.json`, `${tmDst}/grammars/${name}.json`);
}


// 上面那個 php.json 是給純 PHP 用的，缺了 .php 檔需要的 text.html.php grammar（HTML 為主、嵌入 source.php）。
fs.copyFileSync('php-html.tmLanguage.json', `${tmDst}/grammars/php-html.json`);
console.log(`Copied ${GRAMMARS.length + 1} TextMate grammars → ${tmDst}/grammars`);
