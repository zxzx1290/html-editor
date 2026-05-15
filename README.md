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
- Tab bar 右側 `+` 按鈕可新增空白匿名檔案（Untitled），儲存時自動彈出「另存為」對話框選擇目錄與檔名
- 儲存按鈕：有未儲存變更時才可點擊；儲存中顯示「儲存中…」並阻擋重複觸發
- 圖片預覽（`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`）
- 懶載入樹狀目錄，點擊展開子目錄；建立新目錄後自動展開並捲動到位
- 側邊欄寬度可拖曳調整（拖曳分隔線），設定持久化於 localStorage
- 拖曳上傳（可拖入側邊欄或指定目錄）、上傳進度條顯示；上傳前若目的地已有同名檔案，詢問是否覆蓋
- 多選支援：
  - Ctrl+Click 逐一選取 / 取消
  - Shift+Click 範圍選取
  - 多選後可統一刪除、剪下、複製；選取的子項目若已被選取的父目錄涵蓋，自動過濾不重複操作
- 右鍵選單：
  - 空白處：新增檔案、新增目錄、上傳、貼上（有剪貼板內容時）、重新整理
  - 目錄：新增檔案、新增目錄、上傳到此處、剪下、複製、貼上（有剪貼板內容時）、重新命名、重新整理、刪除目錄
  - 檔案：開啟、下載、剪下、複製、貼上（有剪貼板內容時，貼至父目錄）、重新命名、刪除
- 鍵盤快捷鍵：Ctrl+S 儲存（匿名檔案則彈出另存為）、Delete 刪除選取的檔案或目錄
- 重新命名對話框自動反白主檔名（不含副檔名），方便直接輸入新名稱
- IndexedDB session 還原：重新整理後自動恢復上次開啟的 tab 與未儲存草稿；session 還原後自動展開樹狀目錄至 active 檔案所在位置
- 快取衝突偵測：session 還原時若檔案已被他人修改，提示選擇保留草稿或使用伺服器版本
- 編輯器設定（儲存於 localStorage）：
  - **主題**：Dark、Light、HC Dark、HC Light（內建）；Monokai、Dracula、Nord、Cobalt2、Solarized Dark、Solarized Light、Tomorrow Night、Tomorrow Night Eighties（擴充）
  - **字體**：預設、Consolas、Menlo、Courier New、Roboto Mono
  - **字體大小**：10–32 px
  - **Minimap**：開／關切換
  - **顯示隱形字元（Whitespace）**：開／關切換
  - **儲存時移除尾端空白**：開（預設）／關切換
- 預設啟用自動換行（Word Wrap）
- TOTP 二步驟登入：以 `config.json` 設定帳號，每位使用者擁有獨立 workspace
- Session 以 JWT（HS256）儲存於 `editorToken` cookie，無伺服器端 session 記錄；`jwtSecret` 必須於 `config.json` 設定，否則程式拒絕啟動
- Session 驗證時同時確認帳號仍存在於 `config.json`，從 config 移除的帳號下次請求即自動失效
- Session 自動延長：前端每 60 秒呼叫 `/check`；TTL 不足 `sessionTTL / 2` 時伺服器自動延長並回傳新 JWT cookie，回應中 `extended: true` 時顯示提示
- WebSocket 即時協作：
  - 使用者上下線廣播（`user_online` / `user_offline`）
  - 檔案開啟與關閉廣播（`file_opened` / `file_closed`）
  - 多人同時開啟同一檔案時互相通知（`same_file_open`）
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
| `host` | 監聽 host（`0.0.0.0` 表示允許外部連線，預設 `127.0.0.1`） |
| `port` | 監聽 port（預設 `8080`） |
| `sessionTTL` | session 有效期（秒）；預設 86400（24 小時） |
| `maxUploadSize` | 單檔上傳上限（bytes）；預設 52428800（50 MB） |
| `title` | 瀏覽器標籤與頁面顯示名稱；預設 `HTML Editor` |
| `rateLimitWindow` | 失敗次數計算的時間視窗（秒）；預設 300 |
| `rateLimitMaxAttempts` | 視窗內最大失敗次數；達到後觸發封鎖；預設 5 |
| `rateLimitBanDuration` | 觸發封鎖後的封鎖時長（秒）；預設同 `rateLimitWindow` |
| `jwtSecret` | JWT 簽署金鑰（**必填**）；建議使用長度 32 字元以上的隨機字串 |
| `users.<name>.totpSecret` | TOTP 金鑰（Base32），可用 Google Authenticator 等 App 掃碼 |
| `users.<name>.workspace` | 該使用者的 workspace 目錄 |

登入頁面（`/login`）要求輸入帳號與 TOTP 驗證碼。同一 IP 在 `rateLimitWindow` 秒內登入失敗達 `rateLimitMaxAttempts` 次，將被封鎖 `rateLimitBanDuration` 秒。

## 目錄結構

```
html-editor/
├── main.go               # Go 後端
├── go.mod
├── go.sum
├── config.json           # 必要，TOTP 登入與伺服器設定（不進 git）
├── config.example.json   # 設定範例
├── package.json          # 僅用於安裝靜態資源
├── setup-monaco.js       # 建置腳本（npm install 時自動執行）
├── static/
│   ├── index.html        # 前端（Vue 3 + Monaco，單一 HTML 檔案）
│   ├── login.html        # 登入頁面
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
config.json
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
| `POST` | `/api/rename?from=&to=` | 重新命名或移動；目的地已存在回傳 409（`?auto=1` 時自動加序號） |
| `POST` | `/api/copy?from=&to=` | 複製檔案或目錄（遞迴）；目的地已存在自動加序號 |
| `GET` | `/api/config` | 回傳 `{ username?: string }`；`username` 在 session 有效時附上目前登入帳號 |
| `POST` | `/login` | 登入（form: username, code） |
| `GET` | `/logout` | 登出並清除 `editorToken` cookie |
| `GET` | `/check` | 回傳 `{ "data": <剩餘秒數>, "extended": bool }`；TTL 不足 `sessionTTL / 2` 時自動延長並寫入新 JWT cookie；無效 token 回傳 401 |
| `GET` | `/ws` | WebSocket 連線；用於使用者上下線與同檔案開啟互相通知 |

所有路徑均以 workspace 為根目錄，後端會阻擋路徑逃逸（`../` 等）。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援（點擊後顯示預覽而非開啟編輯器）：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`

單檔上傳上限：50 MB（可透過 config.json `maxUploadSize` 調整）。
