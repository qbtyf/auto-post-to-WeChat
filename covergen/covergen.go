// Package covergen 封面动态生成器（v1.4 新增）。
//
// 按文章标题 + 当天日期实时绘制公众号封面（900×383，2.35:1），
// 提供 4 种风格：终端风 / 简报风 / 科技渐变风 / 极简黑白风。
//
// 【重要避坑记录】（2026-10-05 实测教训）
//   - 不要用 gg 的 SetFillStyle(渐变)+Clear()：Clear 只认纯色，画布会保持
//     初始全透明状态（部分查看器渲染成"空白"）。渐变必须逐行插值绘制。
//   - 微软雅黑是 TTC 集合格式，必须用 golang.org/x/image/font/opentype 的
//     ParseCollection 加载（gg 自带的 LoadFontFace 不支持 ttc）。
package covergen

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"sync"
	"time"

	"github.com/fogleman/gg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
)

// colorRGBA 简写别名。
type colorRGBA = color.RGBA

// Styles 支持的封面风格列表（下拉框用，第一项为默认）。
var Styles = []string{"终端风", "简报风", "科技渐变风", "极简黑白风"}

// BrandText 封面上的公众号名。
const BrandText = "老齐的周报"

const (
	width  = 900
	height = 383

	FontRegular = `C:\Windows\Fonts\msyh.ttc`   // 微软雅黑（常规）
	FontBold    = `C:\Windows\Fonts\msyhbd.ttc` // 微软雅黑（粗体）
)

// ---------- 字体加载与缓存 ----------

var (
	fontMu    sync.Mutex
	fontCache = map[string]*opentype.Font{} // 路径 → 字体对象（读一次 19MB 文件，后续复用）
)

func loadFont(path string) (*opentype.Font, error) {
	fontMu.Lock()
	defer fontMu.Unlock()
	if f, ok := fontCache[path]; ok {
		return f, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读字体失败 %s: %w", path, err)
	}
	col, err := opentype.ParseCollection(data)
	if err != nil {
		return nil, fmt.Errorf("解析 TTC 集合失败 %s: %w", path, err)
	}
	f, err := col.Font(0) // face 0 = 主字体
	if err != nil {
		return nil, err
	}
	fontCache[path] = f
	return f, nil
}

// face 生成指定路径 + 字号的字体 face。
// 注意：face 非线程安全，本包设计为 UI 线程串行使用。
func face(path string, size float64) (font.Face, error) {
	f, err := loadFont(path)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

// fitTitle 标题过长时自动缩小字号（从 startSize 每次减 4，最低 28），返回可用的 face。
func fitTitle(dc *gg.Context, title string, maxW, startSize float64, bold bool) font.Face {
	path := FontRegular
	if bold {
		path = FontBold
	}
	for size := startSize; size >= 28; size -= 4 {
		f, err := face(path, size)
		if err != nil {
			continue
		}
		dc.SetFontFace(f)
		if w, _ := dc.MeasureString(title); w <= maxW {
			return f
		}
	}
	f, _ := face(path, 28)
	return f
}

// ---------- 对外 API ----------

// Generate 按风格与标题绘制封面，返回图像（日期自动取当天）。
func Generate(styleName, title string) (image.Image, error) {
	dateStr := time.Now().Format("2006-01-02")
	dc := gg.NewContext(width, height)
	var err error
	switch styleName {
	case "终端风":
		err = drawTerminal(dc, title, dateStr)
	case "简报风":
		err = drawBrief(dc, title, dateStr)
	case "科技渐变风":
		err = drawTech(dc, title, dateStr)
	case "极简黑白风":
		err = drawMinimal(dc, title, dateStr)
	default:
		err = fmt.Errorf("未知封面风格: %s", styleName)
	}
	if err != nil {
		return nil, err
	}
	return dc.Image(), nil
}

// GeneratePNG 生成封面并写入 PNG 文件。
func GeneratePNG(styleName, title, outPath string) error {
	img, err := Generate(styleName, title)
	if err != nil {
		return err
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

// ---------- 风格 1：终端风（GitHub 深色，移植自 Z 盘 Python 脚本） ----------

func drawTerminal(dc *gg.Context, title, dateStr string) error {
	dc.SetHexColor("#0d1117")
	dc.Clear()

	// 顶部小字：公众号名 · WEEKLY
	f, _ := face(FontBold, 22)
	dc.SetFontFace(f)
	dc.SetHexColor("#7ee787")
	dc.DrawString(BrandText+" · WEEKLY", 45, 70)

	// 大标题（自适应字号）
	dc.SetColor(hexColor("#ffffff"))
	tf := fitTitle(dc, title, 520, 46, true)
	dc.SetFontFace(tf)
	dc.DrawString(title, 45, 160)

	// 副标语
	sf, _ := face(FontRegular, 22)
	dc.SetFontFace(sf)
	dc.SetHexColor("#8b949e")
	dc.DrawString("精选整理 · 每周更新", 45, 220)

	// 日期徽章（绿框圆角）
	bf, _ := face(FontRegular, 24)
	dc.SetFontFace(bf)
	dc.SetHexColor("#7ee787")
	w, _ := dc.MeasureString(dateStr)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(45, 255, w+28, 46, 10)
	dc.Stroke()
	dc.DrawString(dateStr, 59, 287)

	// 右侧伪终端窗口
	dc.SetHexColor("#161b22")
	dc.DrawRoundedRectangle(590, 55, 265, 270, 12)
	dc.Fill()
	dc.SetLineWidth(1)
	dc.SetHexColor("#30363d")
	dc.DrawRoundedRectangle(590, 55, 265, 270, 12)
	dc.Stroke()
	dots := []string{"#ff5f56", "#ffbd2e", "#27c93f"}
	for i, c := range dots {
		dc.SetHexColor(c)
		dc.DrawCircle(614+float64(i)*22, 82, 6)
		dc.Fill()
	}
	cmds := []struct{ p, t, c string }{
		{"$ ", "git log --weekly", "#8b949e"},
		{"  ", "top10 → weekly.mp", "#79c0ff"},
		{"  ", "stars: 462,5660", "#7ee787"},
		{"$ ", "hot  trend-up", "#8b949e"},
		{"  ", "publish: ready", "#f0883e"},
	}
	cf, _ := face(FontRegular, 17)
	dc.SetFontFace(cf)
	for i, cmd := range cmds {
		dc.SetHexColor("#7ee787")
		dc.DrawString(cmd.p, 610, 130+float64(i)*36)
		pw, _ := dc.MeasureString(cmd.p)
		dc.SetHexColor(cmd.c)
		dc.DrawString(cmd.t, 610+pw+4, 130+float64(i)*36)
	}
	return nil
}

// ---------- 风格 2：简报风（米白纸质 + 红色刊头） ----------

func drawBrief(dc *gg.Context, title, dateStr string) error {
	dc.SetHexColor("#faf6ef")
	dc.Clear()

	dc.SetHexColor("#c0392b")
	dc.DrawRectangle(0, 0, width, 8)
	dc.Fill()
	f, _ := face(FontBold, 24)
	dc.SetFontFace(f)
	dc.DrawString(BrandText, 45, 72)
	rf, _ := face(FontRegular, 20)
	dc.SetFontFace(rf)
	tw, _ := dc.MeasureString(dateStr)
	dc.DrawString(dateStr, width-45-tw, 70)

	dc.SetHexColor("#1a1a1a")
	tf := fitTitle(dc, title, 810, 52, true)
	dc.SetFontFace(tf)
	dc.DrawString(title, 45, 200)

	dc.SetLineWidth(1.5)
	dc.SetHexColor("#c0392b")
	dc.DrawLine(45, 300, width-45, 300)
	dc.Stroke()
	dc.SetFontFace(rf)
	dc.DrawString("专注分享 · 欢迎关注", 45, 340)
	return nil
}

// ---------- 风格 3：科技渐变风（深蓝→青，逐行插值，绝不透明） ----------

func drawTech(dc *gg.Context, title, dateStr string) error {
	// 【避坑】不能用 SetFillStyle(grad)+Clear()（Clear 只认纯色，画布会全透明）。
	top := [3]float64{0x1a, 0x29, 0x80}
	bot := [3]float64{0x26, 0xd0, 0xce}
	for y := 0; y < height; y++ {
		t := float64(y) / float64(height-1)
		dc.SetColor(colorRGBA{
			R: uint8(top[0] + (bot[0]-top[0])*t),
			G: uint8(top[1] + (bot[1]-top[1])*t),
			B: uint8(top[2] + (bot[2]-top[2])*t),
			A: 255,
		})
		dc.DrawRectangle(0, float64(y), width, 1)
		dc.Fill()
	}

	f, _ := face(FontRegular, 22)
	dc.SetFontFace(f)
	dc.SetColor(hexColor("#ffffff"))
	dc.DrawString(BrandText, 45, 70)

	tf := fitTitle(dc, title, 700, 54, true)
	dc.SetFontFace(tf)
	dc.DrawString(title, 45, 190)

	// 装饰：右侧同心圆弧（半透明白）
	dc.SetLineWidth(2)
	for i := 1; i <= 4; i++ {
		dc.SetRGBA(1, 1, 1, 0.12)
		dc.DrawCircle(810, 300, float64(i)*32)
		dc.Stroke()
	}

	dc.SetFontFace(f)
	dc.SetLineWidth(2)
	dc.SetRGBA(1, 1, 1, 0.8)
	w, _ := dc.MeasureString(dateStr)
	dc.DrawRoundedRectangle(45, 250, w+28, 46, 23)
	dc.Stroke()
	dc.DrawString(dateStr, 59, 282)
	return nil
}

// ---------- 风格 4：极简黑白风 ----------

func drawMinimal(dc *gg.Context, title, dateStr string) error {
	dc.SetHexColor("#f5f5f3")
	dc.Clear()

	dc.SetHexColor("#111111")
	dc.DrawRectangle(45, 55, 10, 273)
	dc.Fill()

	tf := fitTitle(dc, title, 730, 60, true)
	dc.SetFontFace(tf)
	dc.DrawString(title, 90, 190)

	f, _ := face(FontRegular, 22)
	dc.SetFontFace(f)
	dc.SetHexColor("#888888")
	tw, _ := dc.MeasureString(dateStr)
	dc.DrawString(dateStr, width-45-tw, 330)
	return nil
}

// ---------- 工具 ----------

func hexColor(s string) (c colorRGBA) {
	fmt.Sscanf(s, "#%02x%02x%02x", &c.R, &c.G, &c.B)
	c.A = 255
	return
}
