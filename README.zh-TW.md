<!-- 翻譯自 README.md。只翻 "For AI agents" 以上的章節，英文版更新時一起改。 -->

<div align="center">

<h1>NYCU</h1>

<p><strong>把陽明交大 E3 和課表變成遠端 MCP server，給 ChatGPT、Claude 和其他 agent 用。</strong></p>

[![CI](https://img.shields.io/github/actions/workflow/status/yorukot/nycu/check.yml?branch=main&style=flat-square&label=CI)](https://github.com/yorukot/nycu/actions)
[![Go](https://img.shields.io/github/go-mod/go-version/yorukot/nycu?style=flat-square)](go.mod)
[![License](https://img.shields.io/github/license/yorukot/nycu?style=flat-square)](LICENSE)

[English](README.md) | **繁體中文**

</div>

```console
$ timetable_lookup_courses {"cos_ids": [516706, "535702"]}
{"data":[{"cos_id":"516706","name":"向量微積分","time":"R56-SA320[GF]",...},
         {"cos_id":"535702","name":"資料探勘","time":"T56-EC115[GF]",...}],
 "metadata":{"acysem":"1151","count":2,"missing":[]}}
```

陽明交大的課業資訊分散在 E3（Moodle）和公開課表兩個地方。這個 server 給 agent 17 個唯讀工具，同時涵蓋兩邊。它是一個 Go 執行檔，不需要資料庫，設計上部署在 Dokploy，再透過 OAuth 接到 ChatGPT（包含 Dots）。

## 功能

- **讀 E3**：課程、作業、期限、公告與回覆、最近的變動、通知、測驗、繳交狀態、成績、課程大綱和檔案下載（13 個工具）。
- **查公開課表**：學期、課程搜尋、用 cos_id 查課和節次表（4 個工具）。
- **遠端 MCP**：在 `/mcp` 提供 Streamable HTTP，可以接 ChatGPT 的自訂 MCP server、Claude Code 和其他 MCP client。
- **內建 OAuth 2.1**：支援動態註冊 client、PKCE 和 refresh token。每個 client 用同一組擁有者密碼核准，伺服器不存任何資料。
- **唯讀**：沒有任何工具會寫入 E3。token 不會出現在工具輸出、錯誤訊息或 log 裡。

## 快速開始

1. **在自己的電腦上取得 E3 token**。指令會提示輸入密碼，輸入時不顯示，密碼只會送到 E3。

   ```sh
   go run github.com/yorukot/nycu/cmd/nycu@latest e3-token
   ```

   在 clone 下來的 repo 裡執行 `go run ./cmd/nycu e3-token` 也可以。它會印出 `E3_TOKEN=…` 和 `E3_USERID=…`。
2. **部署到 Dokploy**：
   1. 建立一個 Application，來源選 GitHub 的 `yorukot/nycu`，分支 `main`，Build type 選 **Dockerfile**。
   2. 填入[設定](#設定)裡的環境變數。`AUTH_SECRET` 用 `openssl rand -base64 48` 產生。
   3. 在 **Domains** 加上你的網域，開啟 HTTPS，container port 填 `8080`。`PUBLIC_URL` 設成同一個 `https://` 網址。
   4. 部署。完成後 `https://<網域>/healthz` 應該回傳 `ok`。
3. **接上 ChatGPT**：
   1. 在 ChatGPT 打開 **Plugins**，按 **+**，選 **Add custom MCP server**。依方案不同，可能要先開啟開發者模式才能加自訂 server。
   2. URL 填 `https://<網域>/mcp`，驗證方式選 **OAuth**。
   3. ChatGPT 會開啟 server 的授權頁，在那裡輸入 `OWNER_PASSWORD`。
   4. 之後 Dot 就能使用這個 plugin。可以問它：「這週有哪些 E3 作業要交？」
4. **或接上 Claude Code**：執行 `claude mcp add --transport http nycu https://<網域>/mcp`，再用 `/mcp` 登入。

## 使用方式

| 服務 | 工具 |
|------|------|
| E3 | `e3p_whoami`、`e3p_list_courses`、`e3p_list_assignments`、`e3p_list_due`、`e3p_list_announcements`、`e3p_get_discussion`、`e3p_list_updates`、`e3p_list_notifications`、`e3p_list_quizzes`、`e3p_get_submission`、`e3p_list_grades`、`e3p_get_content`、`e3p_download_file` |
| 課表 | `timetable_list_semesters`、`timetable_search_courses`、`timetable_lookup_courses`、`timetable_get_periods` |

- 每個結果的格式都一樣：成功時是 `{"data": …, "metadata": {…}}`；失敗時是 `{"error": {"message", "why", "hint"}}`，並帶有 `isError`。
- `cos_id` 是 E3 課程 shortname 點號後面那一段。例如 `1151.535702` 代表 acysem 是 `1151`、cos_id 是 `535702`，所以可以直接拿 `e3p_list_courses` 的結果去呼叫 `timetable_lookup_courses`。
- `e3p_download_file` 接受從 `e3p_get_content`、`e3p_list_announcements` 或 `e3p_get_discussion` 拿到的 `pluginfile.php` URL，並把檔案以 embedded resource 回傳：文字檔回傳文字，其他檔案回傳 base64。
- 字串太長時結果會被截短。文字中會出現 `truncated` 標記，結果也會多一個 `_truncation` 欄位，列出被截短的欄位，並說明怎麼縮小查詢範圍。

## 設定

| 變數 | 必填 | 預設 | 用途 |
|------|------|------|------|
| `PUBLIC_URL` | 是 | | 對外網址，例如 `https://nycu.example.com`，OAuth metadata 會用到。 |
| `OWNER_PASSWORD` | 是 | | 授權頁的密碼，至少 12 個字元。 |
| `AUTH_SECRET` | 是 | | 簽署 client ID、code 和 token 的金鑰，至少 32 bytes。換掉它會讓所有登入失效。 |
| `E3_TOKEN` | 是 | | 用 `nycu e3-token` 取得的 E3 Web Services token。 |
| `E3_USERID` | 否 | 自動查詢 | 你的 E3 使用者 ID，設定後可以省一次查詢。 |
| `E3_BASE` | 否 | `https://e3p.nycu.edu.tw` | Moodle 網站根網址。 |
| `PORT` | 否 | `8080` | 監聽的 port。 |
| `E3_DOWNLOAD_MAX_BYTES` | 否 | `10485760` | `e3p_download_file` 能回傳的最大檔案大小。 |
| `MCP_MAX_STRING_CHARS` / `MCP_MAX_TOTAL_CHARS` | 否 | `20000` / `120000` | 單次結果的截短上限。 |

E3 token 可以讀取你整個 E3 帳號，請只放在 Dokploy 的環境變數裡。要撤銷它，到 E3 的 **偏好設定 → 安全金鑰** 重設金鑰，再重新執行 `nycu e3-token`。

## 運作方式

- **無狀態的 server**：一個 Go 程序同時提供 `/mcp`、OAuth endpoints 和 `/healthz`。MCP 跑在無狀態的 Streamable HTTP 模式。
- **沒有帶 token 的請求**：沒有有效 access token 的 `/mcp` 請求會收到 401，`WWW-Authenticate` header 指向 `/.well-known/oauth-protected-resource/mcp`。client 從那裡找到授權 server，並自行註冊。
- **簽章值**：client ID、code 和 token 都用 `AUTH_SECRET` 做 HMAC 簽章。code 有效 1 分鐘，access token 1 小時，refresh token 30 天，而且 refresh token 每次使用後都會換新。用過的 code、用過的 refresh token 和輸錯的密碼次數都記在記憶體裡。同一個位址 15 分鐘內輸錯 5 次密碼，就會被鎖 15 分鐘。
- **呼叫 E3**：server 透過 Moodle Web Services REST API 存取 E3，token 放在 POST body 裡。需要對每門課、每份作業或每個測驗各呼叫一次的操作，最多 4 個同時進行。
- **下載的安全性**：`e3p_download_file` 只會把 token 送到設定好的 E3 網域上、走 HTTPS 的 `pluginfile.php` URL。每一次 redirect 都會用同樣的規則重新檢查。

## 限制

- **只給一個人用**：一個 server 只對應一個 E3 token。多人使用的話，每人各部署一個。
- **只跑一個 replica**：防止 code 重放的紀錄和密碼錯誤次數都存在記憶體，重新啟動就會清空。
- **只有 E3 上的資料**：E3 只會有老師放上去的東西。只在課堂上宣布的期限不會出現在 `e3p_list_due`。
- **沒有入口網站和兼任簽到**：原本 plugin 的入口網站和兼任簽到工具已經移除。

## For AI agents

給 coding agent 和 LLM 工具看的資訊，請見[英文版](README.md#for-ai-agents)。

## 開發

```sh
gofmt -l .
go vet ./...
go test -race ./...
```

測試都用合成資料，所以 CI 不需要 NYCU 帳號。加上 `NYCU_LIVE=1 go test ./internal/...` 會額外呼叫真的課表和 E3；如果同時設定 `E3_TOKEN`，還會跑唯讀的 E3 查詢。

本機執行時，設定 `PUBLIC_URL=http://localhost:8080` 和其他變數，再執行 `go run ./cmd/nycu`。

## 授權

[MIT](LICENSE)。改寫自 [zyx1121/nycu](https://github.com/zyx1121/nycu)。
