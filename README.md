# HTML Editor

基於 Go + Monaco Editor 的網頁檔案編輯器，可離線運作。

## 功能

- **編輯器**：Monaco Editor 搭配 TextMate 語法引擎（`vscode-textmate` + `vscode-oniguruma`），支援 HTML / CSS / JavaScript / TypeScript / JSON / PHP / Vue / Python / Go / Rust / Ruby / Shell / Markdown / C++ / Java / Dockerfile / YAML / SQL，分詞粒度與配色皆與 VS Code 原版一致
- **多分頁**：可拖曳排序、中鍵關閉、未儲存以圓點標示；`+` 按鈕新增空白檔案（Untitled，儲存時彈出「另存為」）或終端機
- **狀態列**：縮排、編碼、行尾序列（LF / CRLF）、語言，皆可點擊切換
- **編碼**：開檔自動偵測（jschardet），支援 UTF-8 / UTF-8 BOM / UTF-16 LE、BE / Big5 / GBK / GB18030 / Shift_JIS / EUC-JP / EUC-KR / Latin-1，也可手動指定重新載入
- **檔案樹**：懶載入、拖曳上傳（進度條、佇列、可取消）、多選（Ctrl / Shift + Click）、剪下／複製／貼上／建立副本／重新命名／刪除、關鍵字過濾、目錄 mtime 輪詢自動刷新、symlink 標示與目標提示
- **搜尋**：資料夾遞迴 regex 搜尋，結果以新分頁串流呈現，對命中行 Ctrl+Click 可跳到該檔該行
- **下載**：檔案直接下載；目錄自動打包成 zip（上限 10000 檔 / 500 MB）
- **圖片預覽**：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`
- **開檔防護**：超過 10 MB 或內容疑似二進位時，先詢問是否仍要開啟
- **快捷鍵**：Ctrl+S 儲存、Ctrl+/ 切換註解、Delete 刪除選取項目
- **設定**（存於 localStorage）：主題（10 種 VS Code 原版配色）、字體與大小、自動換行、Sticky Scroll、括號配對上色、顯示隱形字元、儲存移除行尾空白、HTML/PHP 自訂註解、終端機字體
- **Session 還原**：IndexedDB 保存分頁與未儲存草稿，重整後自動還原；若檔案已被他人修改會提示選擇保留草稿或使用伺服器版本
- **登入**：TOTP 二步驟驗證 + JWT cookie，每個帳號擁有獨立 workspace，登入失敗有 IP rate limit，並可選用 SMTP 登入通知
- **即時協作**：WebSocket 廣播使用者上下線、檔案開啟／關閉，多人開啟同一檔案時互相提示
- **終端機**（Linux / macOS，需帳號開放）：tmux 共享 session，xterm.js + WebGL 呈現，可多開；重新整理或斷線只 detach、重連自動還原，關閉分頁才 kill-session
- **Plugin 系統**：載入 `static/plugins/plugins.json` 列出的插件，詳見下方 [Plugin 系統](#plugin-系統)
- 響應式版面，行動裝置支援側邊欄遮罩；全部資源本機提供，可離線運作

## 環境需求

| 工具 | 版本 |
|------|------|
| [Go](https://go.dev/dl/) | 1.24 以上 |
| [Node.js](https://nodejs.org/) | 18 以上（僅建置用，執行時不需要） |

## 安裝與建置

### 1. 複製靜態資源（只需執行一次）

```bash
npm install
```

`postinstall` 腳本（`setup.js`）會自動完成以下事情：

- 將 Vue 3 global build 複製到 `static/vue.global.js`
- 將 Monaco 靜態檔案複製到 `static/monaco/vs/`
- 將 VS Code 原版語法高亮主題（`tm-themes`）複製到 `static/themes/`
- 將 xterm.js（含 fit addon、unicode11 addon、WebGL addon）複製到 `static/xterm/`
- 以 esbuild 將 `vscode-textmate` + `vscode-oniguruma` 打包為 IIFE，連同 `onig.wasm` 與 TextMate grammar（HTML、HTML derivative、CSS、JavaScript、JSON、PHP `source.php`、Python、Go、Rust、Ruby、Shell（`shellscript`）、Markdown、C++、Java、Dockerfile（`docker`）、YAML、SQL、TypeScript、Vue 來自 `tm-grammars`；`.php` 檔的入口 grammar `text.html.php` vendored 自 vscode `extensions/php/syntaxes/html.tmLanguage.json`）一起輸出到 `static/textmate/`
- 以 esbuild 將 `@vscode/iconv-lite-umd`（編解碼）+ `jschardet`（偵測）打包為 IIFE 輸出到 `static/encoding/`
- 將 Lucide icon font（`lucide-static`）的 `lucide.woff2` 與精簡版 CSS（只引用 woff2）複製到 `static/lucide/`

### 2. 建立 config.json

複製範例並依需求修改：

```bash
cp config.example.json config.json
```

詳細欄位說明見下方 [config.json 設定](#configjson-設定)。

### 3. 編譯 Go 執行檔

```bash
go build .
```

Windows：

```powershell
go build -o html-editor.exe .
```

## 執行

```bash
./html-editor
```

Windows：

```powershell
./html-editor.exe
```

啟動後開啟瀏覽器前往 [http://127.0.0.1:8080](http://127.0.0.1:8080)。

> **注意**：`config.json` 必須存在於執行目錄，否則程式無法啟動。Host、Port 等均在 `config.json` 中設定。

## config.json 設定

```json
{
  "host": "127.0.0.1",
  "port": 8080,
  "sessionTTL": 86400,
  "maxUploadSize": 52428800,
  "watchPollInterval": 3,
  "title": "HTML Editor",
  "rateLimitWindow": 300,
  "rateLimitMaxAttempts": 5,
  "rateLimitBanDuration": 300,
  "jwtSecret": "change-this-to-a-long-random-string",
  "trustProxy": false,
  "logMode": "fmt",
  "logTag": "html-editor",
  "users": {
    "alice": {
      "totpSecret": "JBSWY3DPEHPK3PXP",
      "workspace": "./workspace/alice",
      "terminal": true
    },
    "bob": {
      "totpSecret": "JBSWY3DPEHPK3PXP",
      "workspace": "./workspace/bob"
    }
  }
}
```

| 欄位 | 說明 |
|------|------|
| `host` | 監聽 host（`0.0.0.0` 表示允許外部連線，預設 `127.0.0.1`） |
| `port` | 監聽 port（預設 `8080`） |
| `sessionTTL` | session 有效期（秒）；預設 86400（24 小時） |
| `maxUploadSize` | 單檔上傳上限（bytes）；預設 52428800（50 MB） |
| `watchPollInterval` | 檔案樹目錄變更輪詢間隔（秒）；預設 3 |
| `title` | 瀏覽器標籤與頁面顯示名稱；預設 `HTML Editor` |
| `rateLimitWindow` | 失敗次數計算的時間視窗（秒）；預設 300 |
| `rateLimitMaxAttempts` | 視窗內最大失敗次數；達到後觸發封鎖；預設 5 |
| `rateLimitBanDuration` | 觸發封鎖後的封鎖時長（秒）；預設同 `rateLimitWindow` |
| `jwtSecret` | JWT 簽署金鑰（**必填**）；長度須至少 32 字元，否則程式拒絕啟動 |
| `trustProxy` | 是否信任 `X-Forwarded-For` / `X-Forwarded-Proto`（預設 `false`）；僅在已知信任的 reverse proxy 後方開啟 |
| `logMode` | log 輸出模式：`fmt`（輸出到 stdout，自帶時間戳，預設）或 `syslog`（寫入本機 syslog，時間戳由 syslog 提供）。`syslog` 僅 Linux/macOS 支援，其他系統會 fallback 回 stdout |
| `logTag` | `syslog` 模式的 tag；留空則預設 `html-editor`。僅在 `logMode` 為 `syslog` 時生效 |
| `loginNotify` | 登入成功／失敗時寄送的 SMTP 郵件通知（選填，預設不啟用）；詳見下方 [登入通知](#登入通知-loginnotify) |
| `users.<name>.totpSecret` | TOTP 金鑰（Base32），可用 Google Authenticator 等 App 掃碼 |
| `users.<name>.workspace` | 該使用者的 workspace 目錄 |
| `users.<name>.terminal` | 是否開放此使用者使用 tmux 終端機；預設 `false`。需 Linux/macOS 環境且系統已安裝 `tmux` 才會生效 |

登入頁面（`/login`）要求輸入帳號與 TOTP 驗證碼。同一 IP 在 `rateLimitWindow` 秒內登入失敗達 `rateLimitMaxAttempts` 次，將被封鎖 `rateLimitBanDuration` 秒。

### 登入通知 (loginNotify)

設定 `loginNotify` 後，登入成功或失敗時會在背景透過 SMTP 寄出一封郵件（goroutine 非同步送出，**通知失敗或逾時不會影響登入**）。`host` 留空即停用。

```json
"loginNotify": {
  "host": "smtp.gmail.com",
  "port": 587,
  "username": "notify@example.com",
  "password": "your-smtp-password-or-app-password",
  "tls": "starttls",
  "notifySuccess": true,
  "notifyFailure": true,
  "timeoutSeconds": 5,
  "from": "notify@example.com",
  "to": "you@example.com",
  "subject": "[html-editor] {username} {event} login from {ip}",
  "body": "<h3>html-editor login notification</h3><table><tr><td>User</td><td>{username}</td></tr><tr><td>Event</td><td>{event}</td></tr><tr><td>IP</td><td>{ip}</td></tr><tr><td>Reason</td><td>{reason}</td></tr><tr><td>Time</td><td>{time}</td></tr></table>"
}
```

| 欄位 | 說明 |
|------|------|
| `host` | SMTP 伺服器位址；留空表示停用 |
| `port` | SMTP 連接埠；預設 `587` |
| `username` | SMTP 認證帳號；留空則不做認證 |
| `password` | SMTP 認證密碼（Gmail 等需用「應用程式密碼」） |
| `tls` | `starttls`（預設，連線後升級，常見於 587 埠）、`tls`（隱式 TLS，常見於 465 埠）或 `none`（明文） |
| `notifySuccess` | 登入成功時是否通知 |
| `notifyFailure` | 登入失敗時是否通知（含帳密錯誤、TOTP 重放、IP 封鎖） |
| `timeoutSeconds` | 連線／傳輸逾時秒數；預設 5 |
| `from` | 寄件者地址 |
| `to` | 收件者地址；可用逗號、分號或空白分隔多個 |
| `subject` | 郵件主旨（會做變數代換，非 ASCII 字元會自動編碼） |
| `body` | HTML 郵件內文（`Content-Type: text/html`，會做變數代換；代入的變數值會自動做 HTML 跳脫以防注入） |

**可用變數**（會在送出前代換 `subject` 與 `body` 的值）：

| 變數 | 內容 |
|------|------|
| `{username}` | 登入帳號（IP 封鎖時為空字串） |
| `{ip}` | 來源 IP |
| `{event}` | `success` 或 `failure` |
| `{reason}` | 失敗原因：`invalid`（帳密錯誤）、`replay`（TOTP 重放）、`blocked`（該次失敗剛好觸發 IP 封鎖時送出，每次封鎖僅通知一次，避免被暴力嘗試灌爆）；成功時為空 |
| `{time}` | RFC3339 時間戳記 |

## 目錄結構

```
html-editor/
├── main.go               # Go 後端
├── go.mod
├── go.sum
├── config.json           # 必要，TOTP 登入與伺服器設定（不進 git）
├── config.example.json   # 設定範例
├── package.json          # 僅用於安裝靜態資源
├── setup.js              # 建置腳本（npm install 時自動執行）
├── textmate-entry.js     # esbuild entry，打包 vscode-textmate / vscode-oniguruma
├── php-html.tmLanguage.json  # text.html.php，vendored 自 vscode 官方
├── static/
│   ├── index.html        # 前端（Vue 3 + Monaco，單一 HTML 檔案）
│   ├── login.html        # 登入頁面
│   ├── style.css         # 前端樣式
│   ├── vue.global.js     # Vue 3 runtime（由 npm install 產生，不進 git）
│   ├── monaco/           # Monaco 靜態檔案（由 npm install 產生，不進 git）
│   ├── themes/           # 語法高亮主題 JSON（由 npm install 產生，不進 git）
│   ├── textmate/         # TextMate 引擎、grammar、onig.wasm（由 npm install 產生，不進 git）
│   ├── encoding/         # 編碼偵測／轉換 bundle（由 npm install 產生，不進 git）
│   ├── xterm/            # xterm.js 與 addon（由 npm install 產生，不進 git）
│   ├── lucide/           # Lucide icon font（由 npm install 產生，不進 git）
│   └── plugins/          # Plugin 目錄（不進 git，依環境各自部署）
│       ├── plugins.json  # Plugin 載入清單
│       └── *.js          # 各 plugin 檔案
└── workspace/            # 使用者編輯的檔案（不進 git）
```

## Plugin 系統

編輯器啟動時會自動 fetch `GET /static/plugins/plugins.json`。若該檔案不存在（HTTP 404）則視為無 plugin，靜默略過。

### plugins.json 格式

URL 陣列，每個項目為 plugin JS 檔案的路徑：

```json
[
  "/static/plugins/myplugin.js",
  "/static/plugins/another.js"
]
```

### Plugin 格式

Plugin 為 IIFE，全部 API 收斂在單一命名空間 `window.editor`：

```javascript
(function () {
    // 訂閱生命週期事件
    window.editor.on('fileBeforeSave', function (e) {
        // e.value — 即將儲存的內容；e.path — 檔案路徑
    });

    window.editor.on('fileAfterSave', function (e) {
        // e.path / e.value — 已儲存的路徑與內容；e.document.title — 檔名
    });

    window.editor.on('fileOnOpen', function (e) {
        // e.path — 開啟的檔案路徑；e.value — 檔案內容（session 還原時亦會觸發）
    });

    window.editor.on('fileOnClose', function (e) {
        // e.path — 關閉的檔案路徑
    });

    // 取得目前已開啟的所有檔案路徑
    var tabs = window.editor.getTabs(); // ['dir/a.txt', 'b.js', ...]

    window.editor.notify('訊息文字');            // toast 通知
    window.editor.alert('標題', '內文', '細節'); // 單按鈕警告對話框

    // 直接存取編輯器實體（除錯 / 進階用）
    // window.editor.monaco      — Monaco editor 實體
    // window.editor.terminals   — 終端機實體 Map
    // window._editorApp         — Vue component proxy（非契約，僅供除錯）
})();
```

#### `window.editor` API 一覽

| 成員 | 說明 |
|------|------|
| `on(ev, fn)` / `off(ev, fn)` | 訂閱 / 取消事件：`fileOnOpen`、`fileOnClose`、`fileBeforeSave`、`fileAfterSave` |
| `getTabs()` | 回傳目前已開啟的檔案路徑陣列 |
| `notify(msg)` | toast 通知 |
| `alert(title, body, detail)` | 單按鈕警告對話框 |
| `loadPlugin(url)` | 動態載入另一個 plugin |
| `monaco` / `terminals` | Monaco editor 實體與終端機 Map（getter） |

> **注意**：Plugin 在 session 還原之前載入，因此 `on('fileOnOpen', ...)` 的處理器會在 session 還原時對每個還原的 tab 觸發一次（斷線重連則不會）。

## 部署

將以下檔案複製到伺服器後執行：

```
html-editor（或 html-editor.exe）
config.json
static/
  index.html
  login.html
  style.css
  vue.global.js
  monaco/
  themes/
  textmate/
  encoding/
  lucide/
  xterm/      ← 若有開放終端機則一併部署
  plugins/    ← 若有 plugin 則一併部署
```

`workspace/` 目錄會在首次啟動時自動建立。

## REST API

後端提供以下 API，供前端與 plugin 使用：

| 方法 | 路徑 | 說明 |
|------|------|------|
| `GET` | `/api/files?path=` | 列出目錄內容；回傳 `{ files: [{ path, name, isDir, size, isSymlink?, linkTarget? }] }`，目錄排在前，同層按名稱排序；`isSymlink` 涵蓋 Unix symlink 與 Windows directory junction，`linkTarget` 為 `os.Readlink` 結果（以 `/` 分隔） |
| `GET` | `/api/file?path=` | 讀取檔案 |
| `PUT` | `/api/file?path=` | 寫入檔案（body 為純文字） |
| `DELETE` | `/api/file?path=` | 刪除檔案或目錄 |
| `POST` | `/api/upload` | 上傳檔案（multipart/form-data，上限 50 MB） |
| `GET` | `/api/download?path=` | 下載檔案；path 為目錄時串流打包成 zip（超過 10000 個檔或未壓縮總和 500 MB 回 413），symlink 一律略過不跟隨 |
| `POST` | `/api/mkdir?path=` | 建立目錄（含巢狀） |
| `POST` | `/api/rename?from=&to=` | 重新命名或移動；目的地已存在回傳 409（`?auto=1` 時自動加序號） |
| `POST` | `/api/copy?from=&to=` | 複製檔案或目錄（遞迴）；目的地已存在自動加序號 |
| `POST` | `/api/search` | 資料夾遞迴 regex 搜尋；body `{ "path": "<dir>", "q": "<regex>" }`，以 `application/x-ndjson` 串流回傳 `{type:"file",path,matches:[{line,text}]}` 與最後一筆 `{type:"done",files_scanned,files_matched,total_matches,elapsed_ms,truncated,timeout}`；超過 30 秒會逾時、單檔大於 5 MB 直接跳過、每位使用者同時只允許一個搜尋（並發時回 429） |
| `GET` | `/api/config` | 回傳 `{ username?, terminal? }`；session 有效時附上 `username`（目前登入帳號）與 `terminal`（該帳號是否可用終端機） |
| `POST` | `/login` | 登入（form: username, code） |
| `GET` | `/logout` | 登出並清除 `editorToken` cookie |
| `GET` | `/check` | 回傳 `{ "data": <剩餘秒數>, "extended": bool }`；TTL 不足 `sessionTTL / 2` 時自動延長並寫入新 JWT cookie；無效 token 回傳 401 |
| `GET` | `/ws` | WebSocket 連線；使用者上下線、同檔案開啟互相通知、目錄變更推播（`dir_changed`）與 tmux 終端機 I/O |

所有路徑均以 workspace 為根目錄，後端會阻擋路徑逃逸（`../` 等）。

### Symlink 行為（刻意設計）

路徑逃逸防護採字串比對，擋的是 `../` 這類相對路徑逃逸。**符號連結（symlink）則是刻意允許跟隨的**：`/api/file`、`/api/download`、`/api/search` 會沿著 workspace 內的 symlink 讀取／遍歷，即使目標位於 workspace 之外。這是本專案的預期行為（例如讓使用者以 symlink 掛入共用素材、跨專案引用）。

安全含義：**workspace 內的 symlink 等同信任邊界**。任何能在某使用者 workspace 內建立 symlink 的人（例如具備 `terminal` 權限者），即可讓該 workspace 的其他存取者透過上述 API 讀到 workspace 外的檔案。因此：

- 不要讓多個信任層級不同的使用者共用同一個 workspace；
- 具 `terminal` 權限者本就能存取伺服器上該程序可及的檔案，symlink follow 不構成額外的權限提升；
- 若部署環境要求嚴格隔離，請以作業系統層級手段（獨立帳號、chroot、容器、mount namespace）約束程序本身可觸及的檔案範圍。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援（點擊後顯示預覽而非開啟編輯器）：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`

單檔上傳上限：50 MB（可透過 config.json `maxUploadSize` 調整）。
