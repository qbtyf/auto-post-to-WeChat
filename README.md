# 公众号发稿助手（Go + Fyne 单文件版）

把 Markdown 一键发布到微信公众号**草稿箱**的 Windows 桌面小工具：
自动转换公众号排版样式 → 上传正文图片 → 生成/上传封面 → 创建草稿，全程图形界面、日志可视。

> 单文件 exe、无控制台黑窗、内置 SSH 隧道（应对公众号 IP 白名单）。

## 功能特性

| 功能 | 说明 |
|---|---|
| 📄 Markdown 转公众号样式 | 经典蓝主题、表格对齐迁移、标题 emoji 剥除、摘要 120 字节截断、落款行过滤等 8 条清洗规则 |
| 🎨 封面自动生成 | 内置 4 种风格（终端风/简报风/科技渐变风/极简黑白风），标题 + 当天日期，窗口内实时预览；也可自选本地图片 |
| ✂️ 章节拆分 | 按二级标题（`## `）把一篇 MD 拆成多篇草稿，每篇配专属封面 |
| 🔌 内置 SSH 隧道 | 公众号开启 IP 白名单时，请求经你自己的固定 IP 服务器发出 |
| 🖼 正文图片自动上传 | 本地图片自动传微信素材库并替换为线上地址 |

## 使用前必填：你的 AppID 和 AppSecret

程序本身**不含任何密钥**，发布前需要填入你自己的公众号凭据：

### 第 1 步：获取 AppID / AppSecret

1. 浏览器打开 **mp.weixin.qq.com**，扫码登录你的公众号；
2. 左侧菜单最下方 → **设置与开发** → **基本配置**；
3. 「开发者ID(AppID)」直接可见；
4. 「开发者密码(AppSecret)」点「**生成/重置**」，管理员扫码 + 手机确认后显示——**只显示这一次**，当场复制保存。

> 用测试号也行（mp.weixin.qq.com/debug/cgi-bin/sandbox），接口一致，适合先试跑。

### 第 2 步：填入程序（两种方式任选）

**方式一（简单）**：编辑 `config/config.go`，把两个空字符串改成你的值：

```go
var (
    AppID     = "wx你的appid"
    AppSecret = "你的32位secret"
)
```

**方式二（推荐，密钥不进源码、不进 git 历史）**：编译时注入：

```bash
go build -ldflags "-X wechat-mp-publisher/config.AppID=wx你的appid -X wechat-mp-publisher/config.AppSecret=你的secret" -o 公众号发稿助手.exe .
```

> ⚠️ 安全提醒：AppSecret 等同于公众号的开发者密码，**不要提交到仓库、不要发给别人**。
> 如果不小心泄露，去「基本配置」里重置即可（旧密钥立即作废）。

### 第 3 步（可选）：SSH 隧道配置

公众号后台若开启了「**IP 白名单**」，只有白名单内的 IP 能取 access_token：

- 你的电脑是**固定 IP 且已在白名单**：把 `config/config.go` 里的 `ECSHost` 留空（不启用隧道，直连）；
- 你的电脑是**家用宽带（IP 不固定）**：填入一台你自己的固定 IP 服务器（云主机等）的 SSH 信息，
  并把该服务器出口 IP 加进公众号白名单；本机 `~/.ssh` 有免密私钥时 `ECSPassword` 留空。

## 如何编译

程序用 Go + Fyne（图形库）开发，Windows 下编译需要 Go 和一个 C 编译器（Fyne 依赖 CGo 机制）：

```bash
# 1. 安装 Go 1.22+（https://go.dev/dl 或用 winget: GoLang.Go）

# 2. 安装 GCC（Windows 推荐 WinLibs，一条命令）：
winget install --id BrechtSanders.WinLibs.POSIX.UCRT -e

# 3. 设置国内 Go 模块代理（网络慢时建议）：
go env -w GOPROXY=https://goproxy.cn,direct

# 4. 拉取依赖并编译：
go mod tidy
CGO_ENABLED=1 go build -ldflags "-H=windowsgui" -o 公众号发稿助手.exe .

# 编译期注入密钥（推荐，见上文方式二）则用：
CGO_ENABLED=1 go build -ldflags "-H=windowsgui -X wechat-mp-publisher/config.AppID=wx你的appid -X wechat-mp-publisher/config.AppSecret=你的secret" -o 公众号发稿助手.exe .
```

`-H=windowsgui` 的作用：隐藏控制台黑窗，双击直接出图形界面。

## 使用流程

1. 双击运行 exe；
2. 「📂 选择 Markdown 文件」选一篇 `.md`；
3. 封面三选一：自动生成（选风格，标题留空=自动用文章标题）/ 内嵌周报封面 / 自选图片；
4. 需要按 `##` 拆分多篇时勾选对应选项，填作者名；
5. 「👀 生成预览」浏览器里看排版效果；
6. 「🚀 上传到草稿箱」→ 日志依次显示 token 获取 → 封面上传 → 草稿创建；
7. 到公众号后台「内容与互动 → 草稿箱」检查，确认后手动群发。

## 常见错误码速查

| errcode | 含义 | 处理 |
|---|---|---|
| 40001 | AppSecret 不对 | 检查是否填错 / 是否被重置过 |
| 40164 | IP 不在白名单 | 把出口 IP 加进白名单，或正确配置隧道 |
| 41001 | access_token 缺失 | 程序 bug 或 token 过期，重启重试 |
| 45009 | 素材数量超限 | 清理公众号后台多余素材 |

## 目录结构

```
├── main.go               # 程序入口 + Fyne 界面 + 发布流程
├── theme.go              # 公众号蓝浅色主题
├── config/config.go      # ⚠️ 凭据配置（AppID/AppSecret/SSH）
├── converter/            # Markdown → 公众号 HTML（12 条清洗规则）
├── converttest/          # 转换回归测试工具（不启 GUI 快速验证，附引用块长度指标）
├── covergen/             # 封面绘制（4 风格，系统微软雅黑，零字体依赖）
├── tunnel/               # SSH 隧道（DialContext 直走 SSH 通道）
├── wechat/               # 微信 API（token/上传素材/存草稿）
├── winfd/                # Windows 原生文件对话框封装
└── assets/               # 内嵌默认封面
```

## 技术栈

Go 1.22+ · Fyne v2.8（图形界面）· golang.org/x/crypto/ssh（隧道）· goldmark（Markdown 解析）

## 更新日志

### v2.1（2026-10-06）引用块修复版

- **新增「单个引用块文字量自动拆分」**：微信公众号 2026-10 起的编辑器校验，单个 `<blockquote>` 内纯文字量超过约 450 字会报「多媒体插件校验出错」（普通段落不受限）。转换器现在自动把超长引用块按清单条目换行 → 句号 → 硬切的优先级拆到 400 字以内（`converter.SplitLongBlockquotes`），与实测发表成功的逻辑逐行对齐。

### v2.0（2026-10-06）多媒体修复版

- 对齐全部既有清洗规则：删除 HTML 注释、原生 HTML 直通（goldmark WithUnsafe）、colgroup 列宽迁移、链接转文末脚注、不可见 Unicode 清理等 14 项——修复 v1.x 手写表格全丢导致的编辑器报错
- 新增：站外链接自动转文末「参考链接」、支持「阅读原文」跳转（`config.BlogURL`）、标题 64 字截断

## 免责声明

本工具仅操作你**自己的**公众号（上传素材、创建草稿，不自动群发），请遵守微信公众平台运营规范。
