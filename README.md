# HTML Editor

基於 Go + Monaco Editor 的網頁檔案編輯器，可離線運作，不依賴任何第三方 Go 套件。

## 功能

- Monaco Editor（VS Code 編輯器核心），支援 HTML/CSS/JS/JSON/Markdown/PHP 等語法高亮
- 多 tab 開檔，支援同時編輯多個檔案
- 圖片預覽（PNG、JPG、GIF、WebP、ICO）；SVG 以 XML 格式在編輯器中開啟
- 懶載入樹狀目錄，點擊展開子目錄
- 拖曳上傳、右鍵選單、新增/重新命名/刪除/下載檔案
- Ctrl+S 儲存、Ctrl+W 關閉 tab、未儲存離頁提示
- IndexedDB session 還原：重新整理後自動恢復上次開啟的 tab 與未儲存內容
- 編輯器設定（主題、字體、字體大小、Minimap），儲存於 localStorage
- 可選 HTTP Basic Auth 保護
- 響應式版面

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

| 參數 | 預設值 | 必填 | 說明 |
|------|--------|:----:|------|
| `-host` | `127.0.0.1` | — | 監聽 host（`0.0.0.0` 表示允許外部連線） |
| `-port` | `8080` | — | 監聽 port |
| `-workspace` | `./workspace` | — | 檔案存放目錄，不存在時自動建立 |
| `-username` | `admin` | — | Basic Auth 帳號（需搭配 `-password` 才生效） |
| `-password` | _(空)_ | — | Basic Auth 密碼；**對外部開放時強烈建議設定** |

> **注意**：若未設定 `-password`，任何人皆可存取編輯器，請勿在公開網路上使用預設設定。

## 目錄結構

```
html-editor/
├── main.go               # Go 後端
├── go.mod
├── package.json          # 僅用於安裝靜態資源，不進 production
├── setup-monaco.js       # 建置腳本（npm install 時自動執行）
├── static/
│   ├── index.html        # 前端（單一 HTML 檔案）
│   ├── monaco/           # Monaco 靜態檔案（由 npm install 產生）
│   └── themes/           # 語法高亮主題 JSON（由 npm install 產生）
└── workspace/            # 使用者編輯的檔案（預設）
```

## 部署

將以下檔案一起複製到伺服器：

```
html-editor   （執行檔）
static/       （含 monaco/ 與 themes/ 子目錄）
```

`workspace/` 目錄會在首次啟動時自動建立。

## 支援的檔案類型

伺服器接受任意副檔名，無限制。

前端圖片預覽支援：`.png` `.jpg` `.jpeg` `.gif` `.webp` `.svg` `.ico`（點擊後顯示預覽而非開啟編輯器）。

單檔上傳上限：50 MB。
