# HTML Editor — Go + Monaco Editor 建置規格

## 專案概述

一個供**不信任外部人員**使用的網頁 HTML 編輯器。
- 前端：Monaco Editor（VS Code 編輯器核心），純靜態
- 後端：Go HTTP server，處理檔案讀寫 API
- 無 terminal、無 shell 執行能力
- 多人可同時存取不同檔案

---

## 目錄結構

```
html-editor/
├── main.go
├── go.mod
├── package.json          # 僅用於安裝 monaco-editor，不進 production
├── static/
│   ├── index.html        # 前端（單一 HTML 檔案，內嵌所有 JS/CSS）
│   └── monaco/           # Monaco Editor 本地靜態檔案
│       └── vs/
│           ├── loader.js
│           └── ...
└── workspace/            # 使用者編輯的檔案存放區
```

---

## 功能需求

### 後端 API（Go）

| Method | Path | 說明 |
|--------|------|------|
| `GET` | `/api/files` | 列出 workspace/ 下所有檔案（遞迴） |
| `GET` | `/api/file?path=foo.html` | 讀取檔案內容 |
| `PUT` | `/api/file?path=foo.html` | 儲存檔案內容（body 為純文字） |
| `POST` | `/api/upload` | 上傳檔案（multipart/form-data） |
| `GET` | `/api/download?path=foo.html` | 下載檔案 |
| `POST` | `/api/mkdir?path=dir/subdir` | 建立目錄 |
| `DELETE` | `/api/file?path=foo.html` | 刪除檔案 |
| `GET` | `/` | 回傳 static/index.html |
| `GET` | `/static/*` | 靜態資源（含 Monaco 本地檔案） |

### 安全規則（後端必須實作）

1. **Path traversal 防護**：所有 `path` 參數必須經過 `filepath.Clean`，並確認最終路徑在 `workspace/` 目錄內，否則回傳 403
2. **副檔名白名單**：只允許 `.html`、`.htm`、`.css`、`.js`、`.json`、`.txt`、`.md`、`.svg`、`.png`、`.jpg`、`.jpeg`、`.gif`、`.webp`、`.ico`
3. **檔案大小限制**：單檔上傳最大 50MB
4. **workspace 目錄自動建立**：啟動時若不存在則建立

### 設定（command line flags）

```
-port      監聽 port，預設 8080
-host      監聽 host，預設 127.0.0.1
-workspace workspace 目錄路徑，預設 ./workspace
-password  Basic Auth 密碼（若有設定則啟用驗證）
-username  Basic Auth 帳號，預設 admin
```

### Basic Auth

- 若有傳入 `-password` 則對所有路由啟用 HTTP Basic Auth
- 使用 Go 標準庫實作，不依賴第三方套件

---

## 前端需求（static/index.html）

**單一 HTML 檔案**，內嵌所有 JS/CSS。

### 版面

```
┌─────────────────────────────────────────┐
│  [檔案樹 sidebar]  │  [Monaco Editor]   │
│                    │                    │
│  workspace/        │  <編輯區>          │
│  ├── index.html    │                    │
│  └── about.html    │                    │
│                    │                    │
│  [上傳] [新增目錄] │  [儲存] 檔名顯示  │
└─────────────────────────────────────────┘
```

### 功能

- **檔案樹**：顯示 workspace 內所有檔案，點擊開啟編輯
- **Monaco Editor**：
  - 語言自動偵測（`.html` → html、`.css` → css、`.js` → javascript 等）
  - 圖片檔案（`.png`、`.jpg` 等）點擊後顯示預覽，不開啟編輯器
  - 主題：`vs-dark`
  - 啟用 `wordWrap: "on"`
- **儲存**：按鈕或 `Ctrl+S` 觸發 PUT API
- **上傳**：點擊上傳按鈕選擇本機檔案，上傳至目前目錄
- **下載**：在檔案樹右鍵選單或按鈕觸發
- **刪除**：在檔案樹有刪除按鈕，需二次確認
- **新增目錄**：輸入目錄名稱後建立
- **未儲存提示**：有未儲存變更時，檔名旁顯示 `●`，關閉頁面前提示

### Monaco 本地載入方式

Monaco Editor 靜態檔案由 Go server 托管，**不使用 CDN**，可離線運作。

**建置前執行一次（複製 Monaco 靜態檔案）：**
```bash
npm install monaco-editor
cp -r node_modules/monaco-editor/min/vs static/monaco/vs
```

**前端載入方式：**
```html
<script src="/static/monaco/vs/loader.js"></script>
<script>
  require.config({ paths: { vs: '/static/monaco/vs' }});
  require(['vs/editor/editor.main'], function() {
    // 初始化 editor
  });
</script>
```

### UI 風格

- 深色主題，配色接近 VS Code
- sidebar 寬度 250px，可收合
- 響應式：手機版 sidebar 預設收合

---

## go.mod

```
module html-editor

go 1.24
```

**不使用任何第三方套件**，只用 Go 標準庫。

---

## 編譯

```bash
# 1. 先複製 Monaco 靜態檔案（只需執行一次）
npm install monaco-editor
cp -r node_modules/monaco-editor/min/vs static/monaco/vs

# 2. 編譯 Go 執行檔
go build -o html-editor .
```

產出單一執行檔 `html-editor`，搭配 `static/` 目錄一起部署。

---

## 執行範例

```bash
# 無密碼（內網使用）
./html-editor -host 127.0.0.1 -port 8080

# 有密碼保護
./html-editor -host 0.0.0.0 -port 8080 -username editor -password secret123

# 指定 workspace 目錄
./html-editor -workspace /var/www/html-files
```

---

## 注意事項

- `workspace/` 和 `node_modules/` 需要在 `.gitignore` 中排除
- `static/monaco/` 也建議加入 `.gitignore`，透過建置步驟產生
- 前端所有 API 呼叫都是相對路徑，不 hardcode host
- 錯誤回應統一用 JSON：`{"error": "message"}`
- 成功回應統一用 JSON：`{"ok": true}` 或對應資料
