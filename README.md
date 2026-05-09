# HTML Editor

基於 Go + Monaco Editor 的網頁檔案編輯器，可離線運作，不依賴任何第三方 Go 套件。

## 功能

- Monaco Editor（VS Code 編輯器核心），支援 HTML/CSS/JS/JSON/Markdown/PHP 等語法高亮
- 多 tab 開檔，支援同時編輯多個檔案
- 圖片預覽（PNG、JPG、GIF、WebP、ICO）
- 懶載入樹狀目錄，點擊展開子目錄
- 拖曳上傳、右鍵選單、新增/重新命名/刪除/下載檔案
- Ctrl+S 儲存、Ctrl+W 關閉 tab
- IndexedDB session 還原：重新整理後自動恢復上次開啟的 tab 與未儲存內容
- 快取衝突偵測：session 還原時若檔案已被他人修改，提示選擇保留草稿或使用伺服器版本
- 編輯器設定（主題、字體、字體大小、Minimap），儲存於 localStorage
- 可選 HTTP Basic Auth 保護
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

# 開放外部連線並啟用密碼保護
./html-editor -host 0.0.0.0 -port 8080 -username admin -password yourpassword

# 指定 workspace 目錄
./html-editor -workspace /path/to/your/files
```

啟動後開啟瀏覽器前往 [http://127.0.0.1:8080](http://127.0.0.1:8080)。

## 命令列參數

所有參數皆為選填，未指定時使用預設值。

| 參數 | 預設值 | 說明 |
|------|--------|------|
| `-host` | `127.0.0.1` | 監聽 host（`0.0.0.0` 表示允許外部連線） |
| `-port` | `8080` | 監聽 port |
| `-workspace` | `./workspace` | 檔案存放目錄，不存在時自動建立 |
| `-username` | `admin` | Basic Auth 帳號（需搭配 `-password` 才生效） |
| `-password` | _(空)_ | Basic Auth 密碼；**對外部開放時強烈建議設定** |

> **注意**：若未設定 `-password`，任何人皆可存取編輯器，請勿在公開網路上使用預設設定。

## 目錄結構

```
html-editor/
├── main.go               # Go 後端（單一檔案，無第三方依賴）
├── go.mod
├── package.json          # 僅用於安裝靜態資源
├── setup-monaco.js       # 建置腳本（npm install 時自動執行）
├── static/
│   ├── index.html        # 前端（Vue 3 + Monaco，單一 HTML 檔案）
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
        // e.tab.document.value — 檔案內容
    });

    tabManager.on('tabDestroy', function (e) {
        // e.tab.path — 關閉的檔案路徑
    });

    // 取得目前已開啟的所有 tab
    var tabs = tabManager.getTabs(); // [{ path, document: { title } }, ...]

    // 顯示 toast 通知
    bubble.popup('訊息文字');

    // 顯示單按鈕警告對話框
    alertDia.show('標題', '內文', '細節', function () { /* 關閉後回呼 */ });
})();
```

## 部署

將以下檔案複製到伺服器後執行：

```
html-editor（或 html-editor.exe）
static/
  index.html
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
| `GET` | `/api/files?path=` | 列出目錄內容 |
| `GET` | `/api/file?path=` | 讀取檔案 |
| `PUT` | `/api/file?path=` | 寫入檔案（body 為純文字） |
| `DELETE` | `/api/file?path=` | 刪除檔案或目錄 |
| `POST` | `/api/upload` | 上傳檔案（multipart/form-data，上限 50 MB） |
| `GET` | `/api/download?path=` | 下載檔案（含 Content-Disposition） |
| `POST` | `/api/mkdir?path=` | 建立目錄（含巢狀） |
| `POST` | `/api/rename?from=&to=` | 重新命名或移動 |

所有路徑均以 workspace 為根目錄，後端會阻擋路徑逃逸（`../` 等）。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援（點擊後顯示預覽而非開啟編輯器）：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.ico`

單檔上傳上限：50 MB。
