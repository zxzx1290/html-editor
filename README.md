# HTML Editor

基於 Go + Monaco Editor 的網頁檔案編輯器，可離線運作。

## 功能

- Monaco Editor（VS Code 編輯器核心），語法高亮對應：

  | 副檔名 | 語言 |
  |--------|------|
  | `.html` `.htm` | HTML |
  | `.css` | CSS |
  | `.js` | JavaScript |
  | `.json` | JSON |
  | `.md` | Markdown |
  | `.txt` | Plain Text |
  | `.svg` | XML |
  | `.php` | PHP |
  | `.vue` | Vue |
  | 其他 | Plain Text |

- TextMate 語法引擎：以 `vscode-textmate` + `vscode-oniguruma` 取代 Monaco 內建的 Monarch 分詞器（HTML / CSS / JavaScript / JSON / PHP / Python / Go / Rust / Ruby / Shell / Markdown / C++ / Java / Dockerfile / YAML / SQL / TypeScript / Vue），各語言以 `monaco.languages.onLanguage` 在首次開檔時 lazy 載入對應 grammar，分詞粒度與 VS Code 一致，搭配 VS Code 原版主題顏色／斜體完全對齊

- 多 tab 開檔，支援同時編輯多個檔案；tab 標題顯示圓點代表有未儲存變更
- UI icon 採用 Lucide icon font；資料夾、檔案、圖片於樹狀目錄分色顯示，會依目前主題切換
- 符號連結顯示：偵測 Unix symlink 與 Windows directory junction，於樹狀目錄使用 `folder-symlink` / `file-symlink` 圖示，hover 該節點可看到 `→ 目標路徑`（另存對話框同樣支援）
- Tab bar 右側 `+` 按鈕可新增空白匿名檔案（Untitled），儲存時自動彈出「另存為」對話框選擇目錄與檔名
- 底部狀態列右側顯示目前 tab 的語言（取自 Monaco aliases），點擊可彈出語言選擇器手動指定語法高亮
- 儲存按鈕：有未儲存變更時才可點擊；儲存中顯示「儲存中…」並阻擋重複觸發
- 圖片預覽（`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`）
- 懶載入樹狀目錄，點擊展開子目錄；建立新目錄後自動展開並捲動到位
- 側邊欄寬度可拖曳調整（拖曳分隔線），設定持久化於 localStorage
- 拖曳上傳（可拖入側邊欄或指定目錄）、上傳進度條顯示；上傳前若目的地已有同名檔案，詢問是否覆蓋；多檔上傳併行上限 3（完成一個補一個，其餘顯示「等待中」），上傳中或佇列中的檔案可逐一取消
- 多選支援：
  - Ctrl+Click 逐一選取 / 取消
  - Shift+Click 範圍選取
  - 多選後可統一剪下、複製、刪除；選取的子項目若已被選取的父目錄涵蓋，自動過濾不重複操作
- 右鍵選單：
  - 空白處：新增檔案、新增目錄、上傳檔案、貼上（有剪貼板內容時）、搜尋、重新整理
  - 目錄：新增檔案、新增目錄、上傳到此處、剪下、複製、建立副本、貼上（有剪貼板內容時）、重新命名、搜尋、重新整理、刪除目錄
  - 檔案：新增檔案（同層）、開啟、下載、剪下、複製、建立副本、貼上（有剪貼板內容時，貼至父目錄）、重新命名、刪除
  - 多選：剪下 N 個、複製 N 個、刪除 N 個
  - Tab：關閉、關閉全部（只關一般檔案分頁、不關終端機；未儲存的檔案逐一詢問確認，取消則保留該分頁並繼續處理下一個）
- 鍵盤快捷鍵：Ctrl+S 儲存（匿名檔案則彈出另存為）、Delete 刪除選取的檔案或目錄；Ctrl+/ 切換註解（HTML/PHP 自訂註解開啟時走特製邏輯）
- 「建立副本」：對檔案或資料夾自動產生 `name copy`、`name copy 2`… 序號副本；資料夾較大時會先詢問確認
- 資料夾搜尋（regex）：右鍵目錄或空白處選「搜尋」，輸入 regex（如 `(?i)foo`、`\bbar\b`），結果以新分頁串流呈現；對命中行 Ctrl+Click 可跳到該檔該行；後端為 NDJSON streaming，逾時 30 秒、單檔上限 5 MB、單檔最多 200 筆、總筆數上限 1000；自動略過隱藏目錄與 `node_modules` / `vendor` / `dist`；同一使用者一次只允許一個搜尋進行中
- 重新命名對話框自動反白主檔名（不含副檔名），方便直接輸入新名稱
- IndexedDB session 還原：重新整理後自動恢復上次開啟的 tab 與未儲存草稿；session 還原後自動展開樹狀目錄至 active 檔案所在位置
- 快取衝突偵測：session 還原時若檔案已被他人修改，提示選擇保留草稿或使用伺服器版本
- 編輯器設定（儲存於 localStorage）：
  - **主題**：Dark+、Light+（VS Code 預設）；Monokai、Dracula、Nord、Tokyo Night、One Dark Pro、Solarized Dark、GitHub Dark（深色）；Solarized Light（淺色）。全部來自 `tm-themes`，經 vscode-textmate 引擎以 VS Code 原版 scope selector 算色，預設 Dark+
  - **字體**：預設、Consolas、Menlo、Courier New
  - **字體大小**：10–32 px
  - **自動換行**：開（預設）／關切換
  - **Sticky Scroll**：開（預設）／關切換；捲動時把目前 scope 的父層宣告固定在編輯器頂部
  - **括號配對上色**：開／關切換（預設關）
  - **顯示隱形字元**：開／關切換；同時顯示行尾 LF / CRLF 符號與控制字元
  - **儲存移除空白**：開（預設）／關切換；儲存時移除每行行尾空白
  - **HTML/PHP 自訂註解**：開／關切換（預設關）。開啟時 Ctrl+/ 走自訂邏輯：HTML 用 `<!-- -->` 行註解（跳過已註解行）；PHP 依游標位置自動判斷 —— 在 `<?php`/`<script>` 區段內走 `//`、`<style>` 區段走 block comment、其他則用 `<!-- -->`
  - **終端機字體 / 字體大小**：僅在帳號有開放終端機時顯示
- HTML / PHP / Vue 模式自動補全閉合標籤（輸入 `>` 後自動插入對應的 `</tag>`，void element 除外）
- TOTP 二步驟登入：以 `config.json` 設定帳號，每位使用者擁有獨立 workspace
- Session 以 JWT（HS256）儲存於 `editorToken` cookie，無伺服器端 session 記錄；`jwtSecret` 必須於 `config.json` 設定，否則程式拒絕啟動
- Session 驗證時同時確認帳號仍存在於 `config.json`，從 config 移除的帳號下次請求即自動失效
- Session 自動延長：前端每 60 秒呼叫 `/check`；TTL 不足 `sessionTTL / 2` 時伺服器自動延長並回傳新 JWT cookie，回應中 `extended: true` 時顯示提示
- WebSocket 即時協作：
  - 使用者上下線廣播（`user_online` / `user_offline`）
  - 檔案開啟與關閉廣播（`file_opened` / `file_closed`）
  - 多人同時開啟同一檔案時互相通知（`same_file_open`）
  - 每位使用者限一條連線；斷線後 30 秒自動重連
- tmux 終端機（僅 Linux/macOS）：
  - 啟動時建立共享 socket（寫死 `html-editor`）；伺服器重啟不會殺掉現有 session
  - 設定 `users.<name>.terminal: true` 才開放此功能；`+` 按鈕點擊時改為下拉選單（新增空白檔案 / 新增終端機）
  - 終端機混入既有 tab-bar，可同時多開；以 xterm.js + WebGL renderer（GPU 加速）呈現，啟用標準 Unicode 11 寬字元
  - 終端機 tab 標題加上「終端機」前綴，並顯示 tmux session 名末段以利辨識
  - 終端機 tab 可拖曳排序，順序持久化於 localStorage
  - WebSocket 斷線重連時自動列出該使用者所有 tmux session 並全部還原為 tab
  - 關閉終端機 tab（X 或右鍵「關閉」）會先跳確認，確認後 `kill-session`；重新整理或斷線僅 detach、session 保留
- Plugin 系統：啟動時自動載入 `static/plugins/plugins.json` 列出的插件
- 響應式版面，行動裝置支援側邊欄遮罩

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

- 將 Monaco 靜態檔案複製到 `static/monaco/vs/`
- 將 VS Code 原版語法高亮主題（`tm-themes`）複製到 `static/themes/`
- 將 xterm.js（含 fit addon、unicode11 addon、WebGL addon）複製到 `static/xterm/`
- 以 esbuild 將 `vscode-textmate` + `vscode-oniguruma` 打包為 IIFE，連同 `onig.wasm` 與 TextMate grammar（HTML、HTML derivative、CSS、JavaScript、JSON、PHP `source.php`、Python、Go、Rust、Ruby、Shell（`shellscript`）、Markdown、C++、Java、Dockerfile（`docker`）、YAML、SQL、TypeScript、Vue 來自 `tm-grammars`；`.php` 檔的入口 grammar `text.html.php` vendored 自 vscode `extensions/php/syntaxes/html.tmLanguage.json`）一起輸出到 `static/textmate/`
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
  "title": "HTML Editor",
  "rateLimitWindow": 300,
  "rateLimitMaxAttempts": 5,
  "rateLimitBanDuration": 300,
  "jwtSecret": "change-this-to-a-long-random-string",
  "trustProxy": false,
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
| `title` | 瀏覽器標籤與頁面顯示名稱；預設 `HTML Editor` |
| `rateLimitWindow` | 失敗次數計算的時間視窗（秒）；預設 300 |
| `rateLimitMaxAttempts` | 視窗內最大失敗次數；達到後觸發封鎖；預設 5 |
| `rateLimitBanDuration` | 觸發封鎖後的封鎖時長（秒）；預設同 `rateLimitWindow` |
| `jwtSecret` | JWT 簽署金鑰（**必填**）；長度須至少 32 字元，否則程式拒絕啟動 |
| `trustProxy` | 是否信任 `X-Forwarded-For` / `X-Forwarded-Proto`（預設 `false`）；僅在已知信任的 reverse proxy 後方開啟 |
| `loginNotify` | 登入成功／失敗時發送的對外 HTTP 通知（選填，預設不啟用）；詳見下方 [登入通知](#登入通知-loginnotify) |
| `users.<name>.totpSecret` | TOTP 金鑰（Base32），可用 Google Authenticator 等 App 掃碼 |
| `users.<name>.workspace` | 該使用者的 workspace 目錄 |
| `users.<name>.terminal` | 是否開放此使用者使用 tmux 終端機；預設 `false`。需 Linux/macOS 環境且系統已安裝 `tmux` 才會生效 |

登入頁面（`/login`）要求輸入帳號與 TOTP 驗證碼。同一 IP 在 `rateLimitWindow` 秒內登入失敗達 `rateLimitMaxAttempts` 次，將被封鎖 `rateLimitBanDuration` 秒。

### 登入通知 (loginNotify)

設定 `loginNotify` 後，登入成功或失敗時會在背景發送一個對外 HTTP 請求（goroutine 非同步送出，**通知失敗或逾時不會影響登入**）。`url` 留空即停用。

```json
"loginNotify": {
  "url": "https://api.mailgun.net/v3/YOUR_DOMAIN/messages",
  "method": "POST",
  "notifySuccess": true,
  "notifyFailure": true,
  "timeoutSeconds": 3,
  "basicAuth": "api:key-xxxxxxxxxxxxxxxx",
  "form": {
    "from": "notify@YOUR_DOMAIN",
    "to": "you@example.com",
    "subject": "[html-editor] {username} {event} login from {ip}",
    "text": "user={username} event={event} ip={ip} reason={reason} time={time}"
  }
}
```

| 欄位 | 說明 |
|------|------|
| `url` | 通知目標網址；留空表示停用 |
| `method` | `POST`（預設）或 `GET` |
| `notifySuccess` | 登入成功時是否通知 |
| `notifyFailure` | 登入失敗時是否通知（含帳密錯誤、TOTP 重放、IP 封鎖） |
| `timeoutSeconds` | 請求逾時秒數；預設 5 |
| `basicAuth` | `"使用者:密碼"`，會轉成 `Authorization: Basic` 標頭（Mailgun 用 `api:金鑰`） |
| `headers` | 額外的請求標頭（例如 `Authorization: Bearer ...`） |
| `form` | urlencoded 表單欄位；填了就以 `application/x-www-form-urlencoded` 送出（對接 Mailgun 等郵件服務） |

**送出格式依設定自動切換：**

| 情況 | 送出內容 |
|------|---------|
| 有填 `form` | `application/x-www-form-urlencoded` 表單，欄位值會做變數代換 |
| 無 `form`，`method: POST` | JSON body：`{"event","username","ip","reason","time"}` |
| 無 `form`，`method: GET` | 上述欄位接成 query string：`?event=success&username=...&ip=...` |

**可用變數**（會在送出前代換 `form` 的值）：

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
│   ├── vue.global.js     # Vue 3 runtime
│   ├── monaco/           # Monaco 靜態檔案（由 npm install 產生，不進 git）
│   ├── themes/           # 語法高亮主題 JSON（由 npm install 產生，不進 git）
│   ├── textmate/         # TextMate 引擎、grammar、onig.wasm（由 npm install 產生，不進 git）
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
| `GET` | `/api/download?path=` | 下載檔案（含 Content-Disposition） |
| `POST` | `/api/mkdir?path=` | 建立目錄（含巢狀） |
| `POST` | `/api/rename?from=&to=` | 重新命名或移動；目的地已存在回傳 409（`?auto=1` 時自動加序號） |
| `POST` | `/api/copy?from=&to=` | 複製檔案或目錄（遞迴）；目的地已存在自動加序號 |
| `POST` | `/api/search` | 資料夾遞迴 regex 搜尋；body `{ "path": "<dir>", "q": "<regex>" }`，以 `application/x-ndjson` 串流回傳 `{type:"file",path,matches:[{line,text}]}` 與最後一筆 `{type:"done",files_scanned,files_matched,total_matches,elapsed_ms,truncated,timeout}`；超過 30 秒會逾時、單檔大於 5 MB 直接跳過、每位使用者同時只允許一個搜尋（並發時回 429） |
| `GET` | `/api/config` | 回傳 `{ username?, terminal? }`；session 有效時附上 `username`（目前登入帳號）與 `terminal`（該帳號是否可用終端機） |
| `POST` | `/login` | 登入（form: username, code） |
| `GET` | `/logout` | 登出並清除 `editorToken` cookie |
| `GET` | `/check` | 回傳 `{ "data": <剩餘秒數>, "extended": bool }`；TTL 不足 `sessionTTL / 2` 時自動延長並寫入新 JWT cookie；無效 token 回傳 401 |
| `GET` | `/ws` | WebSocket 連線；用於使用者上下線與同檔案開啟互相通知 |

所有路徑均以 workspace 為根目錄，後端會阻擋路徑逃逸（`../` 等）。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援（點擊後顯示預覽而非開啟編輯器）：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`

單檔上傳上限：50 MB（可透過 config.json `maxUploadSize` 調整）。
