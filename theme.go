// mpTheme：公众号蓝浅色主题（v1.7 新增）。
//
// 设计要点：
//   - 背景改浅灰白 #f4f6f8，替代默认纯白，界面层次更柔和；
//   - 主色（Primary）= 公众号蓝 #1e80ff，与预览分隔条同色——单选圆点、
//     HighImportance 主按钮、输入框聚焦描边全部自动跟随；
//   - 其余颜色全部回落到 Fyne 默认浅色主题，保证可读性。
//
// 实现方式：内嵌官方默认主题，只覆写需要的 Color()，其余（字体/尺寸）原样继承。
package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// mpBlue 公众号蓝（程序内主色，与预览页分隔条一致）。
var mpBlue = color.NRGBA{R: 0x1e, G: 0x80, B: 0xff, A: 0xff}

// mpTheme 覆写默认主题的部分颜色。
type mpTheme struct {
	fyne.Theme // 内嵌默认主题：未覆写的方法全部走默认实现
}

// newMPTheme 构造公众号蓝浅色主题。
func newMPTheme() fyne.Theme {
	return &mpTheme{Theme: theme.DefaultTheme()}
}

// Color 覆写配色（v2.8：常量在 theme 包，签名带明/暗模式）。
func (t *mpTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 0xf4, G: 0xf6, B: 0xf8, A: 0xff} // 浅灰白底
	case theme.ColorNamePrimary:
		return mpBlue // 主色：公众号蓝
	case theme.ColorNameFocus:
		return color.NRGBA{R: 0x1e, G: 0x80, B: 0xff, A: 0x2e} // 聚焦淡蓝
	case theme.ColorNameSelection:
		return color.NRGBA{R: 0x1e, G: 0x80, B: 0xff, A: 0x40} // 文本选中淡蓝
	case theme.ColorNameInputBorder:
		return color.NRGBA{R: 0xc9, G: 0xd2, B: 0xda, A: 0xff} // 输入框描边
	}
	return t.Theme.Color(name, variant)
}
