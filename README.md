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
  | 其他 | Plain Text |

- 多 tab 開檔，支援同時編輯多個檔案；tab 標題顯示 ● 代表有未儲存變更
- 儲存按鈕：有未儲存變更時才可點擊；儲存中顯示「儲存中…」並阻擋重複觸發
- 圖片預覽（`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`）
- 懶載入樹狀目錄，點擊展開子目錄；建立新目錄後自動展開並捲動到位
- 拖曳上傳（可拖入側邊欄或指定目錄）、上傳進度條顯示
- 右鍵選單：
  - 空白處：新增檔案、新增目錄、上傳、重新整理
  - 目錄：新增檔案、新增目錄、上傳到此處、重新命名、重新整理、刪除目錄
  - 檔案：開啟、下載、重新命名、刪除
- Ctrl+S 儲存、Ctrl+W 關閉 tab
- IndexedDB session 還原：重新整理後自動恢復上次開啟的 tab 與未儲存草稿；session 還原後自動展開樹狀目錄至 active 檔案所在位置
- 快取衝突偵測：session 還原時若檔案已被他人修改，提示選擇保留草稿或使用伺服器版本
- 編輯器設定（儲存於 localStorage）：
  - **主題**：Dark、Light、HC Dark、HC Light（內建）；Monokai、Dracula、Nord、Cobalt2、Solarized Dark、Solarized Light、Tomorrow Night、Tomorrow Night Eighties（擴充）
  - **字體**：預設、Consolas、Menlo、Courier New、Roboto Mono
  - **字體大小**：10–32 px
  - **Minimap**：開／關切換
- 預設啟用自動換行（Word Wrap）
- TOTP 二步驟登入（可選）：以 `config.json` 設定帳號，每位使用者擁有獨立 workspace
- Session 自動延長：前端每 60 秒檢查剩餘時間，TTL 不足 12 小時時自動呼叫 `/extend` 延長
- WebSocket 即時協作（需啟用 config）：
  - 多人同時開啟同一檔案時互相通知
  - 每位使用者限一條連線；斷線後 30 秒自動重連
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

`postinstall` 腳本（`setup-monaco.js`）會自動將 Monaco 靜態檔案複製到 `static/monaco/vs/`，並將語法高亮主題複製到 `static/themes/`。

### 2. 編譯 Go 執行檔

```bash
go build .
```

Windows：

```powershell
go build -o html-editor.exe .
```

## 執行

```bash
# 本機使用（無密碼）
./html-editor

# 指定 workspace 目錄
./html-editor -workspace /path/to/your/files

# 啟用 TOTP 登入（需先建立 config.json，見下方說明）
./html-editor -config config.json

# 開放外部連線並指定 port
./html-editor -host 0.0.0.0 -port 8080 -config config.json
```

啟動後開啟瀏覽器前往 [http://127.0.0.1:8080](http://127.0.0.1:8080)。

## 命令列參數

所有參數皆為選填，未指定時使用預設值。

| 參數 | 預設值 | 說明 |
|------|--------|------|
| `-host` | `127.0.0.1` | 監聽 host（`0.0.0.0` 表示允許外部連線） |
| `-port` | `8080` | 監聽 port |
| `-workspace` | `./workspace` | 無 config 時的檔案存放目錄，不存在時自動建立 |
| `-config` | _(空)_ | config.json 路徑；若省略且當前目錄存在 `config.json` 則自動載入 |

> **注意**：未設定 `-config` 時，任何人皆可存取編輯器，請勿在公開網路上使用預設設定。

## config.json 設定

`config.json` 用於啟用 TOTP 登入與多使用者 workspace 隔離：

```json
{
  "host": "127.0.0.1",
  "port": 8080,
  "sessionTTL": 86400,
  "maxUploadSize": 52428800,
  "users": {
    "alice": {
      "totpSecret": "JBSWY3DPEHPK3PXP",
      "workspace": "./workspace/alice"
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
| `host` | 監聽 host（覆蓋 `-host` flag，CLI flag 若有明確指定則優先） |
| `port` | 監聽 port（覆蓋 `-port` flag，CLI flag 若有明確指定則優先） |
| `sessionTTL` | session 有效期（秒）；預設 86400（24 小時） |
| `maxUploadSize` | 單檔上傳上限（bytes）；預設 52428800（50 MB） |
| `users.<name>.totpSecret` | TOTP 金鑰（Base32），可用 Google Authenticator 等 App 掃碼 |
| `users.<name>.workspace` | 該使用者的 workspace 目錄 |

登入頁面（`/login`）要求輸入帳號與 TOTP 驗證碼。同一 IP 5 分鐘內登入失敗 5 次將被暫時封鎖。

## 目錄結構

```
html-editor/
├── main.go               # Go 後端
├── go.mod
├── go.sum
├── config.json           # 可選，TOTP 登入設定（不進 git）
├── config.example.json   # 設定範例
├── package.json          # 僅用於安裝靜態資源
├── setup-monaco.js       # 建置腳本（npm install 時自動執行）
├── static/
│   ├── index.html        # 前端（Vue 3 + Monaco，單一 HTML 檔案）
│   ├── login.html        # 登入頁面（啟用 config.json 時使用）
│   ├── vue.global.js     # Vue 3 runtime
│   ├── monaco/           # Monaco 靜態檔案（由 npm install 產生，不進 git）
│   ├── themes/           # 語法高亮主題 JSON（由 npm install 產生，不進 git）
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

Plugin 為 IIFE，透過 `window.editorPlugin.services` 取得各服務：

```javascript
(function () {
    var save       = window.editorPlugin.services.save;
    var tabManager = window.editorPlugin.services.tabManager;
    var bubble     = window.editorPlugin.services['notification.bubble'];
    var alertDia   = window.editorPlugin.services['dialog.alert'];

    // 訂閱事件
    save.on('beforeSave', function (e) {
        // e.value  — 即將儲存的檔案內容
        // e.path   — 檔案路徑
    });

    save.on('afterSave', function (e) {
        // e.path            — 已儲存的檔案路徑
        // e.document.title  — 檔名
        // e.value           — 儲存後的內容
    });

    tabManager.on('open', function (e) {
        // e.tab.path           — 開啟的檔案路徑
        // e.tab.document.value — 檔案內容（初次開啟時為實際內容；session 還原時亦會觸發）
    });

    tabManager.on('tabDestroy', function (e) {
        // e.tab.path — 關閉的檔案路徑
    });

    // 取得目前已開啟的所有 tab（document.value 永遠為空字串，請透過 open 事件取得內容）
    var tabs = tabManager.getTabs(); // [{ path, document: { title, value: '' } }, ...]

    // 顯示 toast 通知
    bubble.popup('訊息文字');

    // 顯示單按鈕警告對話框
    alertDia.show('標題', '內文', '細節', function () { /* 關閉後回呼 */ });
})();
```

#### 可用服務一覽

| 服務名稱 | 說明 |
|----------|------|
| `save` | beforeSave / afterSave 事件 |
| `tabManager` | open / tabDestroy 事件；`getTabs()` |
| `notification.bubble` | `popup(msg)` — toast 通知 |
| `dialog.alert` | `show(title, body, detail, cb)` — 警告對話框 |

> **注意**：Plugin 在 session 還原之前載入，因此 `tabManager.on('open', ...)` 的處理器會在 session 還原時對每個還原的 tab 觸發一次。

## 部署

將以下檔案複製到伺服器後執行：

```
html-editor（或 html-editor.exe）
config.json   ← 若啟用 TOTP 登入
static/
  index.html
  login.html
  vue.global.js
  monaco/
  themes/
  plugins/    ← 若有 plugin 則一併部署
```

`workspace/` 目錄會在首次啟動時自動建立。

## REST API

後端提供以下 API，供前端與 plugin 使用：

| 方法 | 路徑 | 說明 |
|------|------|------|
| `GET` | `/api/files?path=` | 列出目錄內容；回傳 `{ files: [{ path, name, isDir, size }] }`，目錄排在前，同層按名稱排序 |
| `GET` | `/api/file?path=` | 讀取檔案 |
| `PUT` | `/api/file?path=` | 寫入檔案（body 為純文字） |
| `DELETE` | `/api/file?path=` | 刪除檔案或目錄 |
| `POST` | `/api/upload` | 上傳檔案（multipart/form-data，上限 50 MB） |
| `GET` | `/api/download?path=` | 下載檔案（含 Content-Disposition） |
| `POST` | `/api/mkdir?path=` | 建立目錄（含巢狀） |
| `POST` | `/api/rename?from=&to=` | 重新命名或移動 |
| `GET` | `/api/config` | 回傳 `{ sessionCheck: bool }`，前端據此決定是否啟用登入流程 |
| `POST` | `/login` | 登入（form: username, code） |
| `GET` | `/logout` | 登出並清除 session cookie |
| `GET` | `/check` | 回傳 `{ "data": <剩餘秒數> }`；需啟用 config，無 session 回傳 401 |
| `POST` | `/extend` | 延長 session 有效期；需啟用 config |
| `GET` | `/ws` | WebSocket 連線；需啟用 config，用於同檔案開啟互相通知 |

所有路徑均以 workspace 為根目錄，後端會阻擋路徑逃逸（`../` 等）。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援（點擊後顯示預覽而非開啟編輯器）：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`

單檔上傳上限：50 MB（可透過 config.json `maxUploadSize` 調整）。
