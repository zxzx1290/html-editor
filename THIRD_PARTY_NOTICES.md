# 第三方元件聲明（Third-Party Notices）

本專案在 repo 內收錄（vendored），或經由 `setup.js` 複製／打包到 `static/` 並由伺服器提供給瀏覽器的第三方元件，列於下方。各元件依其原始授權條款使用，著作權歸原作者所有。授權全文放在文末；原文以英文為準。

## 收錄在 repo 內的檔案

### `php-html.tmLanguage.json`（`text.html.php` grammar）

取自 VS Code 的 `extensions/php/syntaxes/html.tmLanguage.json`，由 `setup.js` 複製為 `static/textmate/grammars/php-html.json`。來源鏈：

| 層級 | 專案 | 授權 | 著作權 |
|---|---|---|---|
| 直接來源 | [microsoft/vscode](https://github.com/microsoft/vscode) | MIT | Copyright (c) 2015 - present Microsoft Corporation |
| VS Code 的轉換來源 | [KapitanOczywisty/language-php](https://github.com/KapitanOczywisty/language-php)（衍生自 atom/language-php） | MIT | Copyright (c) 2014 GitHub Inc. |
| 更上游 | [textmate/php.tmbundle](https://github.com/textmate/php.tmbundle) | TextMate bundle 寬鬆授權（見文末） | — |

## 複製／打包到 `static/` 的套件

| 元件 | 版本 | 授權 | 著作權 | 在本專案的位置 |
|---|---|---|---|---|
| [monaco-editor](https://github.com/microsoft/monaco-editor) | 0.55.1 | MIT | Copyright (c) 2016 - present Microsoft Corporation | `static/monaco/` |
| [vue](https://github.com/vuejs/core) | 3.5.34 | MIT | Copyright (c) 2018-present, Yuxi (Evan) You | `static/vue.global.js` |
| [@xterm/xterm](https://github.com/xtermjs/xterm.js) | 6.0.0 | MIT | Copyright (c) 2017-2019, The xterm.js authors | `static/xterm/` |
| @xterm/addon-fit | 0.11.0 | MIT | Copyright (c) 2019, The xterm.js authors | `static/xterm/` |
| @xterm/addon-unicode11 | 0.9.0 | MIT | Copyright (c) 2019, The xterm.js authors | `static/xterm/` |
| @xterm/addon-webgl | 0.19.0 | MIT | Copyright (c) 2018, The xterm.js authors | `static/xterm/` |
| [vscode-textmate](https://github.com/microsoft/vscode-textmate) | 9.3.2 | MIT | Copyright (c) Microsoft Corporation | `static/textmate/textmate.js` |
| [vscode-oniguruma](https://github.com/microsoft/vscode-oniguruma) | 2.0.1 | MIT | Copyright (c) Microsoft Corporation | `static/textmate/textmate.js`、`onig.wasm` |
| └ [Oniguruma](https://github.com/kkos/oniguruma)（編譯進 `onig.wasm`） | 6.9.5_rev1 | BSD-2-Clause | Copyright (c) 2002-2020 K.Kosako | `static/textmate/onig.wasm` |
| [@vscode/iconv-lite-umd](https://github.com/microsoft/vscode-iconv-lite-umd) | 0.7.1 | MIT | Copyright (c) Microsoft Corporation | `static/encoding/encoding.js` |
| [jschardet](https://github.com/aadsm/jschardet) | 3.1.4 | **LGPL-2.1-or-later** | António Afonso 等（移植自 Mozilla Universal Charset Detector） | `static/encoding/encoding.js` |
| [lucide-static](https://github.com/lucide-icons/lucide) | 1.16.0 | ISC | Copyright (c) 2026 Lucide Icons and Contributors | `static/lucide/` |
| └ Feather 衍生圖示 | — | MIT | Copyright (c) 2013-present Cole Bemis | `static/lucide/` |
| [tm-grammars](https://github.com/shikijs/textmate-grammars-themes) | 1.31.15 | MIT（彙整）| Copyright (c) 2021 Pine Wu | `static/textmate/grammars/` |
| [tm-themes](https://github.com/shikijs/textmate-grammars-themes) | 1.12.2 | MIT（彙整）| Copyright (c) 2021 Pine Wu | `static/themes/` |

Monaco Editor 本身也內含其他第三方元件。

### jschardet（LGPL-2.1-or-later）

jschardet 由 esbuild 打包進 `static/encoding/encoding.js`，本專案未修改其原始碼。原始碼取得處：<https://github.com/aadsm/jschardet>。使用者可以換掉 `package.json` 裡的 jschardet 版本（或改用自行修改的版本），再執行 `npm install`（會觸發 `setup.js`），重新產生 `encoding.js`。授權全文：<https://www.gnu.org/licenses/old-licenses/lgpl-2.1.html>。

### TextMate grammar（`static/textmate/grammars/`）

tm-grammars 只是彙整，各 grammar 仍依原作者的授權：

| Grammar | 來源 | 授權 | 著作權 |
|---|---|---|---|
| html、html-derivative、css、scss、less、javascript、json、php、python、go、rust、ruby、shellscript、markdown、cpp、java、docker、sql、typescript | microsoft/vscode `extensions/*/syntaxes/` | MIT | Copyright (c) 2015 - present Microsoft Corporation |
| vue | [vuejs/language-tools](https://github.com/vuejs/language-tools) | MIT | Copyright (c) 2021-present Johnson Chu |
| sass | [TheRealSyler/vscode-sass-indented](https://github.com/TheRealSyler/vscode-sass-indented) | MIT | Copyright (c) 2016-2019 Robin Bentley, 2019-present Leonard Grosoli |
| yaml | [textmate/yaml.tmbundle](https://github.com/textmate/yaml.tmbundle) | TextMate bundle 寬鬆授權（見文末） | — |

### 編輯器主題（`static/themes/`）

| 主題 | 授權 | 著作權 |
|---|---|---|
| dark-plus、light-plus、monokai、solarized-dark、solarized-light | MIT | Copyright (c) 2015 - present Microsoft Corporation |
| dracula | MIT | Copyright (c) 2016 Dracula Theme |
| nord | MIT | Copyright (c) 2016-present Sven Greb |
| tokyo-night | MIT | Copyright (c) 2018-present Enkia |
| one-dark-pro | MIT | Copyright (c) 2013-2022 Binaryify |
| github-dark | MIT | Copyright (c) 2020 Primer |

---

## 授權全文

### MIT License

適用於上表所有標示 MIT 的元件；各元件的著作權聲明見上表。

```
Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### ISC License（Lucide）

```
Copyright (c) 2026 Lucide Icons and Contributors

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

### BSD 2-Clause License（Oniguruma）

```
Copyright (c) 2002-2020  K.Kosako  <kkosako0@gmail.com>
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions
are met:
1. Redistributions of source code must retain the above copyright
   notice, this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright
   notice, this list of conditions and the following disclaimer in the
   documentation and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE AUTHOR AND CONTRIBUTORS ``AS IS'' AND
ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED.  IN NO EVENT SHALL THE AUTHOR OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS
OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT
LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY
OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF
SUCH DAMAGE.
```

### TextMate bundle 授權（php.tmbundle、yaml.tmbundle）

```
Permission to copy, use, modify, sell and distribute this software is
granted. This software is provided "as is" without express or implied
warranty, and with no claim as to its suitability for any purpose.
```

### GNU LGPL 2.1 or later（jschardet）

全文：<https://www.gnu.org/licenses/old-licenses/lgpl-2.1.html>
