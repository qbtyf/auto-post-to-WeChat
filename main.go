// 公众号发稿助手 · 单文件 exe 版
//
// 一个把 Markdown 一键发布到微信公众号草稿箱的 Windows 桌面工具：
//   1. Markdown → 公众号排版样式（8 条清洗规则）
//   2. 封面自动生成（4 风格）/ 内嵌默认封面 / 自选图片
//   3. 内置 SSH 隧道（应对公众号 IP 白名单，可留空直连）
//   4. Fyne 桌面窗口，凭据见 config/config.go（使用前必填）
//
// 【使用流程】
//   选 MD 文件 → 选封面方案 →（可选）按章节拆分 → 生成预览 → 上传到草稿箱
package main

import (
	_ "embed" // go:embed 资源嵌入必需（[]byte 场景用空白导入）
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"wechat-mp-publisher/config"
	"wechat-mp-publisher/converter"
	"wechat-mp-publisher/covergen"
	"wechat-mp-publisher/tunnel"
	"wechat-mp-publisher/wechat"
	"wechat-mp-publisher/winfd"
)

// 封面三方案（v1.4；v1.6 缩短文案适配横排单选按钮）。
const (
	coverModeAuto     = "自动生成（推荐）"
	coverModeEmbedded = "内嵌周报封面"
	coverModeCustom   = "自选图片"
)

//go:embed assets/默认封面-终端风.png
var defaultCover []byte // 规则 8 封面双方案之 A：嵌入的终端风默认封面

func main() {
	a := app.New()
	a.Settings().SetTheme(newMPTheme()) // 【v1.7】公众号蓝浅色主题（theme.go）
	w := a.NewWindow("公众号发稿助手 · 单文件版")
	w.Resize(fyne.NewSize(720, 680))

	// ================= 界面状态 =================
	mdPath := ""    // 所选 Markdown 文件
	coverPath := "" // 自选封面（封面方案 B）

	// ================= 日志区 =================
	logEntry := widget.NewMultiLineEntry()
	logEntry.Wrapping = fyne.TextWrapWord
	logEntry.SetPlaceHolder("操作日志会显示在这里……")
	logf := func(format string, args ...any) {
		line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, args...)
		fyne.Do(func() { logEntry.SetText(logEntry.Text + line + "\n") })
	}

	// ================= 第一行：选 MD 文件 =================
	mdLabel := widget.NewLabel("（未选择 Markdown 文件）")
	mdLabel.Wrapping = fyne.TextWrapWord
	btnMD := widget.NewButton("📂 选择 Markdown 文件", func() {
		// 【v1.5】改用 Windows 原生对话框：Fyne 自绘对话框在 200% DPI 下
		// 有列表命中偏移与随机卡死（官方 issue #5531/#4752）
		path, err := winfd.AskOpenFile("选择 Markdown 文件", "Markdown 文件", []string{"*.md"}, "所有文件", []string{"*.*"})
		if err != nil {
			logf("❌ %v", err)
			return
		}
		if path == "" { // 用户取消
			return
		}
		mdPath = path
		mdLabel.SetText(mdPath)
		logf("已选择 MD：%s", mdPath)
	})
	btnMD.Importance = widget.HighImportance // 所有操作按钮统一显眼

	// ================= 封面三方案（v1.6：内嵌单选按钮组，弃用下拉弹层） =================
	// 【为什么不用 Select 下拉框】Fyne 的弹层（PopUp：下拉列表/浮层菜单）在 Windows
	// 高 DPI 缩放（本机 200%）下存在坐标错位 bug——高亮项偏离鼠标（实测见截图）。
	// 而 4×4 网格按钮实测坐标全部精准（点击验证.py 自动化测试），说明**内嵌控件没问题**，
	// 因此全部改用内嵌 RadioGroup 横排单选，彻底绕开弹层。
	coverModeRadio := widget.NewRadioGroup([]string{coverModeAuto, coverModeEmbedded, coverModeCustom}, nil)
	coverModeRadio.Horizontal = true
	coverModeRadio.SetSelected(coverModeAuto)

	// 自动生成方案：风格选择 + 封面标题 + 窗口内预览
	styleRadio := widget.NewRadioGroup(covergen.Styles, nil)
	styleRadio.Horizontal = true
	styleRadio.SetSelected(covergen.Styles[0]) // 默认终端风

	coverTitleEntry := widget.NewEntry()
	coverTitleEntry.SetPlaceHolder("封面标题（留空 = 自动用每篇文章的标题）")

	coverPreview := canvas.NewImageFromImage(nil)
	coverPreview.FillMode = canvas.ImageFillContain
	coverPreview.SetMinSize(fyne.NewSize(330, 141))
	coverPreview.Hide() // 【v1.7】生成预览前隐藏，避免空占位撑出一片白

	btnCoverPreview := widget.NewButton("🎨 预览封面（窗口内）", func() {
		title := coverTitleEntry.Text
		if title == "" { // 留空时取第一篇文章标题
			arts, err := loadArticles(mdPath, false, logf)
			if err != nil {
				logf("❌ %v", err)
				return
			}
			title = arts[0].Title
			coverTitleEntry.SetText(title)
		}
		img, err := covergen.Generate(styleRadio.Selected, title)
		if err != nil {
			logf("❌ 封面生成失败：%v", err)
			return
		}
		coverPreview.Image = img
		canvas.Refresh(coverPreview)
		coverPreview.Show() // 有图了才显示
		logf("🎨 封面预览已生成（%s ·《%s》）", styleRadio.Selected, title)
	})
	btnCoverPreview.Importance = widget.HighImportance // 【v1.8】与主按钮同级别显眼

	autoBox := container.NewVBox(
		container.NewHBox(widget.NewLabel("风格："), styleRadio),
		coverTitleEntry,
		btnCoverPreview,
		coverPreview,
	)
	// 切换方案时显示/隐藏自动生成区
	coverModeRadio.OnChanged = func(mode string) {
		if mode == coverModeAuto {
			autoBox.Show()
		} else {
			autoBox.Hide()
		}
	}

	// 自选方案：文件选择
	coverLabel := widget.NewLabel("")
	coverLabel.Wrapping = fyne.TextWrapWord
	btnCover := widget.NewButton("🖼 选择封面图片", func() {
		// 【v1.5】原生对话框（同上）
		path, err := winfd.AskOpenFile("选择封面图片", "图片文件", []string{"*.png;*.jpg;*.jpeg"}, "所有文件", []string{"*.*"})
		if err != nil {
			logf("❌ %v", err)
			return
		}
		if path == "" {
			return
		}
		coverPath = path
		coverLabel.SetText(coverPath)
		coverModeRadio.SetSelected(coverModeCustom)
		logf("已选择封面：%s", coverPath)
	})
	btnCover.Importance = widget.HighImportance // 【v1.8】与主按钮同级别显眼

	// ================= 选项行 =================
	authorEntry := widget.NewEntry()
	authorEntry.SetText(config.DefaultAuthor)
	splitCheck := widget.NewCheck("按二级标题（## ）拆分为多篇草稿", func(bool) {})

	// ================= 预览 =================
	btnPreview := widget.NewButton("👀 生成预览（浏览器打开）", func() {
		arts, err := loadArticles(mdPath, splitCheck.Checked, logf)
		if err != nil {
			logf("❌ %v", err)
			return
		}
		// 拼一个预览页：每篇之间加标题分隔条
		var b strings.Builder
		b.WriteString("<meta charset='utf-8'><title>公众号发稿预览</title><body style='max-width:760px;margin:0 auto;font-family:微软雅黑'>")
		for i, art := range arts {
			if len(arts) > 1 {
				fmt.Fprintf(&b, "<p style='background:#1e80ff;color:#fff;padding:8px 14px;border-radius:6px;'>第 %d / %d 篇：《%s》</p>", i+1, len(arts), art.Title)
			}
			b.WriteString(art.HTML)
			b.WriteString("<hr style='margin:40px 0;'>")
		}
		b.WriteString("</body>")

		// 【2026-10-05 修复】用 explorer 直接打开本地文件（走系统默认浏览器）。
		// 原方案 OpenURL(file:///...) 失败：临时文件名含中文，经系统调用编码转换后路径丢失。
		// 临时文件名改为纯 ASCII 双保险。
		tmp := filepath.Join(os.TempDir(), "mp_preview.html")
		if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
			logf("❌ 预览文件写入失败：%v", err)
			return
		}
		if err := exec.Command("explorer", tmp).Start(); err != nil {
			logf("❌ 打开浏览器失败：%v", err)
			return
		}
		logf("已生成预览（共 %d 篇）：%s", len(arts), tmp)
	})
	btnPreview.Importance = widget.HighImportance // 【v1.8】与主按钮同级别显眼

	// ================= 上传到草稿箱 =================
	btnPublish := widget.NewButton("🚀 上传到草稿箱", func() { // 按钮先创建占位
	})
	btnPublish.Importance = widget.HighImportance // 【v1.7】主按钮：公众号蓝底白字加大
	btnPublish.OnTapped = func() {
		if mdPath == "" {
			// 【v1.6】不再用 dialog.ShowError（也是弹层，同样有坐标风险），改写日志
			logf("❌ 请先选择 Markdown 文件")
			return
		}
		btnPublish.Disable()
		go func() {
			defer fyne.Do(btnPublish.Enable)
			publishFlow(mdPath, splitCheck.Checked, authorEntry.Text,
				coverModeRadio.Selected, styleRadio.Selected, coverTitleEntry.Text, coverPath, logf)
		}()
	}

	// ================= 布局 =================
	bold := fyne.TextStyle{Bold: true} // 章节标题加粗样式
	form := container.NewVBox(
		widget.NewLabelWithStyle("① Markdown 文件", fyne.TextAlignLeading, bold),
		container.NewHBox(btnMD, mdLabel),

		widget.NewLabelWithStyle("② 封面（三方案）", fyne.TextAlignLeading, bold),
		coverModeRadio,
		autoBox,
		container.NewHBox(btnCover, coverLabel),

		widget.NewLabelWithStyle("③ 选项", fyne.TextAlignLeading, bold),
		container.NewHBox(widget.NewLabel("作者："), authorEntry),
		splitCheck,

		widget.NewLabelWithStyle("④ 执行", fyne.TextAlignLeading, bold),
		container.NewHBox(btnPreview, btnPublish),
		widget.NewLabelWithStyle("日志", fyne.TextAlignLeading, bold),
	)
	split := container.NewVSplit(form, logEntry)
	split.Offset = 0.62
	w.SetContent(split)
	w.ShowAndRun()
}

// loadArticles 读取 MD 并转换为文章列表（预览与上传共用）。
func loadArticles(mdPath string, split bool, logf func(string, ...any)) ([]converter.Article, error) {
	if mdPath == "" {
		return nil, fmt.Errorf("请先选择 Markdown 文件")
	}
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return nil, fmt.Errorf("读取 MD 失败：%w", err)
	}
	arts := converter.Convert(string(raw), split)
	if len(arts) == 0 {
		return nil, fmt.Errorf("转换结果为空，请检查 MD 内容")
	}
	logf("转换完成：共 %d 篇（标题：%s）", len(arts), strings.Join(titles(arts), "、"))
	return arts, nil
}

// publishFlow 完整发布流程：建隧道 → 微信客户端 → 逐篇上传 → 存草稿。
// v1.4 封面三方案：coverModeAuto（每篇按标题+风格实时生成）/ coverModeEmbedded（内嵌图）/ coverModeCustom（自选文件）。
func publishFlow(mdPath string, split bool, author, coverMode, coverStyle, coverTitle, coverPath string, logf func(string, ...any)) {
	logf("—— 开始发布流程 ——")

	// ---- 第 1 步：内置 SSH 隧道（决策点 3） ----
	logf("🔌 正在建立 SSH 隧道 → %s:%s ...", config.ECSHost, config.ECSPort)
	t, err := tunnel.Start(config.ECSHost, config.ECSPort, config.ECSUser, config.ECSPassword)
	if err != nil {
		logf("❌ 隧道建立失败：%v", err)
		return
	}
	defer t.Close()
	logf("✅ 隧道就绪（请求将从 ECS 出口 IP 发出，通过公众号 IP 白名单）")

	// 走 SSH 隧道的 HTTP 客户端（DialContext 直接在 SSH 通道上拨号，
	// 2026-10-05 重构：绕开 SOCKS5 层的"先读假 EOF"坑）
	hc := t.NewHTTPClient(120 * time.Second)

	// ---- 第 2 步：微信客户端（拿 access_token） ----
	if config.AppID == "" || config.AppSecret == "" {
		logf("❌ AppID / AppSecret 未填：请编辑 config/config.go 或用 -ldflags 编译注入后重新打包")
		return
	}
	client, err := wechat.NewClient(config.AppID, config.AppSecret, hc, logf)
	if err != nil {
		logf("❌ %v", err)
		return
	}

	// ---- 第 3 步：转换 Markdown ----
	arts, err := loadArticles(mdPath, split, logf)
	if err != nil {
		logf("❌ %v", err)
		return
	}
	mdDir := filepath.Dir(mdPath)

	// ---- 第 5 步：逐篇上传（封面在循环内准备：自动模式每篇专属） ----
	for i, art := range arts {
		logf("—— 第 %d/%d 篇：《%s》——", i+1, len(arts), art.Title)

		// 5.0 准备本篇封面
		var coverFile string
		switch coverMode {
		case coverModeAuto:
			t := coverTitle
			if t == "" {
				t = art.Title // 留空 = 每篇用各自标题（拆分多篇时每篇封面都不同）
			}
			tmp := filepath.Join(os.TempDir(), fmt.Sprintf("cover_%d.png", i))
			if err := covergen.GeneratePNG(coverStyle, t, tmp); err != nil {
				logf("❌ 封面生成失败：%v", err)
				return
			}
			coverFile = tmp
			logf("  🎨 封面已生成（%s ·《%s》）", coverStyle, t)
		case coverModeCustom:
			if coverPath == "" {
				logf("❌ 请先选择本地封面图片")
				return
			}
			coverFile = coverPath
		default: // coverModeEmbedded
			tmp := filepath.Join(os.TempDir(), "默认封面-终端风.png")
			if err := os.WriteFile(tmp, defaultCover, 0644); err != nil {
				logf("❌ 默认封面解包失败：%v", err)
				return
			}
			coverFile = tmp
			logf("  使用内嵌 GitHub 周报封面")
		}

		// 5.1 正文里的本地图片全部上传并替换为微信 URL
		html, n, err := client.ReplaceLocalImages(art.HTML, mdDir)
		if err != nil {
			logf("❌ %v", err)
			return
		}
		if n > 0 {
			logf("  共替换 %d 张正文图片", n)
		}

		// 5.2 封面上传为永久素材（每篇都要一个 thumb_media_id）
		thumb, err := client.UploadCover(coverFile)
		if err != nil {
			logf("❌ 封面上传失败：%v", err)
			return
		}
		logf("  🖼 封面已上传")

		// 5.3 新增草稿
		if err := client.AddDraft(art.Title, author, art.Digest, html, thumb); err != nil {
			logf("❌ %v", err)
			return
		}
	}

	logf("🎉 全部完成！请到公众号后台「内容与互动 → 草稿箱」检查后手动群发。")
}

// titles 取文章标题列表（日志用）。
func titles(arts []converter.Article) []string {
	out := make([]string, len(arts))
	for i, a := range arts {
		out[i] = a.Title
	}
	return out
}

// uriToPath 把 Fyne 文件对话框返回的 URI 转成 Windows 本地路径。
// Windows 下 file:// URI 形如 file:///C:/xxx，需要去掉开头的斜杠。
func uriToPath(u fyne.URI) string {
	p := u.Path()
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' { // "/C:/xxx" → "C:/xxx"
		return p[1:]
	}
	return p
}
