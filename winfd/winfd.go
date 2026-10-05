// Package winfd 调用 Windows 原生「打开文件」对话框（comdlg32.dll）。
//
// 【为什么不用 Fyne 自绘对话框】Windows 200% DPI 缩放实测：
// Fyne 文件对话框存在侧栏列表命中偏移（点 D: 高亮 E:）与随机卡死
// （选中后"打开/取消"无响应，官方 issue #5531、#4752）。
// 原生对话框由 Windows 自绘渲染，DPI 适配与交互行为和资源管理器完全一致。
package winfd

import (
	"syscall"
	"unsafe"
)

// OPENFILENAMEW 结构（x64，字段顺序与 C 声明一致，Go 按类型自然对齐）。
type openFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

const (
	ofnExplorer        = 0x00080000 // 资源管理器样式
	ofnFileMustExist   = 0x00001000 // 文件必须存在
	ofnNoChangeDir     = 0x00000008 // 不改变进程当前目录
	ofnForceShowHidden = 0x10000000
	ofnNoDpiScalePrompt = 0x02000000 // 由系统处理 DPI，不再弹提示
)

var (
	comdlg32            = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileName = comdlg32.NewProc("GetOpenFileNameW")
	user32              = syscall.NewLazyDLL("user32.dll")
	procGetActiveWindow = user32.NewProc("GetActiveWindow")
)

// AskOpenFile 弹出原生「打开文件」对话框，返回选中的完整路径。
//
// filters 为成对的（描述, 通配符），通配符用分号分隔多扩展名，例如：
//
//	winfd.AskOpenFile("选择 Markdown 文件", "Markdown 文件", []string{"*.md"}, "所有文件", []string{"*.*"})
//
// 用户取消时返回 ("", nil)。
func AskOpenFile(title string, filterPairs ...any) (string, error) {
	// 拼 filter 缓冲：描述\0通配\0描述\0通配\0\0（成对出现）
	var filterBuf []uint16
	pairCount := len(filterPairs) / 2
	for i := 0; i < pairCount; i++ {
		desc, _ := filterPairs[i*2].(string)
		pat, _ := filterPairs[i*2+1].(string)
		filterBuf = append(filterBuf, utf16s(desc)...)
		filterBuf = append(filterBuf, utf16s(pat)...)
	}
	filterBuf = append(filterBuf, 0) // 双 NUL 结尾（前一个 utf16s 已带一个）

	fileBuf := make([]uint16, 32768) // 最大路径缓冲
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	hwnd, _, _ := procGetActiveWindow.Call() // Fyne 主窗口（回调运行在主线程）

	ofn := &openFileName{
		lStructSize:  uint32(unsafe.Sizeof(openFileName{})),
		hwndOwner:    hwnd,
		lpstrFilter:  &filterBuf[0],
		nFilterIndex: 1,
		lpstrFile:    &fileBuf[0],
		nMaxFile:     uint32(len(fileBuf)),
		lpstrTitle:   titlePtr,
		flags:        ofnExplorer | ofnFileMustExist | ofnNoChangeDir | ofnForceShowHidden | ofnNoDpiScalePrompt,
	}
	ret, _, _ := procGetOpenFileName.Call(uintptr(unsafe.Pointer(ofn)))
	if ret == 0 {
		return "", nil // 用户取消或出错（取消视为正常）
	}
	return utf16ToStr(fileBuf), nil
}

// utf16s 字符串 → UTF-16 切片（含尾部 NUL）。
func utf16s(s string) []uint16 {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		return []uint16{0}
	}
	return u
}

// utf16ToStr UTF-16 缓冲 → 字符串（截取到第一个 NUL）。
func utf16ToStr(buf []uint16) string {
	return syscall.UTF16ToString(buf)
}
