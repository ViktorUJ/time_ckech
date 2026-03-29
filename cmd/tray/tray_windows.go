//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32w   = syscall.NewLazyDLL("user32.dll")
	shell32w  = syscall.NewLazyDLL("shell32.dll")
	kernel32w = syscall.NewLazyDLL("kernel32.dll")
	gdi32w    = syscall.NewLazyDLL("gdi32.dll")

	pMessageBoxW                = user32w.NewProc("MessageBoxW")
	pShellExecuteW              = shell32w.NewProc("ShellExecuteW")
	pEnumWindows                = user32w.NewProc("EnumWindows")
	pGetClassNameW              = user32w.NewProc("GetClassNameW")
	pGetWindowTextLengthW       = user32w.NewProc("GetWindowTextLengthW")
	pGetWindowTextW             = user32w.NewProc("GetWindowTextW")
	pGetWindowThreadProcessId   = user32w.NewProc("GetWindowThreadProcessId")
	pOpenProcess                = kernel32w.NewProc("OpenProcess")
	pQueryFullProcessImageNameW = kernel32w.NewProc("QueryFullProcessImageNameW")
	pCloseHandle                = kernel32w.NewProc("CloseHandle")
	pPostMessageW               = user32w.NewProc("PostMessageW")
	pCreateMutexW               = kernel32w.NewProc("CreateMutexW")
	pRegisterClassExW           = user32w.NewProc("RegisterClassExW")
	pCreateWindowExW            = user32w.NewProc("CreateWindowExW")
	pDefWindowProcW             = user32w.NewProc("DefWindowProcW")
	pGetMessageW                = user32w.NewProc("GetMessageW")
	pTranslateMessage           = user32w.NewProc("TranslateMessage")
	pDispatchMessageW           = user32w.NewProc("DispatchMessageW")
	pDestroyWindow              = user32w.NewProc("DestroyWindow")
	pPostQuitMessage            = user32w.NewProc("PostQuitMessage")
	pSendMessageW               = user32w.NewProc("SendMessageW")
	pSetFocus                   = user32w.NewProc("SetFocus")
	pSetForegroundWindow        = user32w.NewProc("SetForegroundWindow")
	pIsDialogMessageW           = user32w.NewProc("IsDialogMessageW")
	pShowWindow                 = user32w.NewProc("ShowWindow")
	pUpdateWindow               = user32w.NewProc("UpdateWindow")
	pGetSystemMetrics           = user32w.NewProc("GetSystemMetrics")
	pSetWindowPos               = user32w.NewProc("SetWindowPos")
	pCreateIconFromResourceEx   = user32w.NewProc("CreateIconFromResourceEx")
	pGetDpiForSystem            = user32w.NewProc("GetDpiForSystem")
	pCreateFontW                = gdi32w.NewProc("CreateFontW")
)

const mutexName = "Global\\ParentalControlTray_SingleInstance"

// dpiScale возвращает масштабированное значение с учётом DPI.
func dpiScale(val int) int {
	dpi, _, _ := pGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	return val * int(dpi) / 96
}

// setWindowIcon устанавливает иконку песочных часов на окно.
func setWindowIcon(hwnd uintptr) {
	icoData := generateHourglassICO()
	if len(icoData) > 22 {
		bmpData := icoData[22:]
		hIcon, _, _ := pCreateIconFromResourceEx.Call(
			uintptr(unsafe.Pointer(&bmpData[0])), uintptr(len(bmpData)),
			1, 0x00030000, 16, 16, 0)
		if hIcon != 0 {
			pSendMessageW.Call(hwnd, 0x0080, 0, hIcon) // WM_SETICON ICON_SMALL
			pSendMessageW.Call(hwnd, 0x0080, 1, hIcon) // WM_SETICON ICON_BIG
		}
	}
}

// createScaledFont создаёт шрифт Segoe UI масштабированный по DPI.
func createScaledFont() uintptr {
	fontName, _ := syscall.UTF16PtrFromString("Segoe UI")
	fontH := -int(dpiScale(16))
	hFont, _, _ := pCreateFontW.Call(
		uintptr(uint32(fontH)),
		0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(fontName)))
	return hFont
}

// ensureSingleInstance убивает предыдущий экземпляр tray.exe (если есть).
func ensureSingleInstance() {
	name, _ := syscall.UTF16PtrFromString(mutexName)
	h, _, err := pCreateMutexW.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h != 0 && err == syscall.ERROR_ALREADY_EXISTS {
		killOtherTrayInstances()
	}
}

func killOtherTrayInstances() {
	myPID := os.Getpid()
	cmd := exec.Command("taskkill", "/F", "/FI",
		fmt.Sprintf("PID ne %d", myPID), "/IM", "tray.exe")
	_ = cmd.Run()
}

// showMessageBox shows a Windows MessageBox.
func showMessageBox(title, text string) {
	go func() {
		runtime.LockOSThread()

		classCounter++
		clsName := fmt.Sprintf("PCMsgBox_%d", classCounter)
		className, _ := syscall.UTF16PtrFromString(clsName)

		var hMainWnd uintptr

		wndProc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmCommand:
				id := wParam & 0xFFFF
				if id == idOK {
					pDestroyWindow.Call(hMainWnd)
					return 0
				}
			case wmClose:
				pDestroyWindow.Call(hMainWnd)
				return 0
			case wmDestroy:
				pPostQuitMessage.Call(0)
				return 0
			}
			ret, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
			return ret
		})

		type WNDCLASSEX struct {
			Size       uint32
			Style      uint32
			WndProc    uintptr
			ClsExtra   int32
			WndExtra   int32
			Instance   uintptr
			Icon       uintptr
			Cursor     uintptr
			Background uintptr
			MenuName   *uint16
			ClassName  *uint16
			IconSm     uintptr
		}

		wc := WNDCLASSEX{
			Size:       uint32(unsafe.Sizeof(WNDCLASSEX{})),
			WndProc:    wndProc,
			Background: 16,
			ClassName:  className,
		}
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		winW := dpiScale(340)
		// Считаем визуальные строки с учётом переноса.
		charsPerLine := 38 // примерно символов в строке при ширине 340
		lineCount := 0
		for _, line := range strings.Split(text, "\n") {
			if len(line) == 0 {
				lineCount++
			} else {
				lineCount += (len([]rune(line)) + charsPerLine - 1) / charsPerLine
			}
		}
		winH := dpiScale(100) + lineCount*dpiScale(22)
		if winH < dpiScale(160) {
			winH = dpiScale(160)
		}

		titlePtr, _ := syscall.UTF16PtrFromString(title)
		hMainWnd, _, _ = pCreateWindowExW.Call(
			wsExTopmost,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(titlePtr)),
			wsOverlapped|wsCaption|wsSysMenu,
			uintptr(cwUseDefault), uintptr(cwUseDefault), uintptr(winW), uintptr(winH),
			0, 0, 0, 0)

		setWindowIcon(hMainWnd)

		hFont := createScaledFont()

		labelH := lineCount * dpiScale(22)
		staticClass, _ := syscall.UTF16PtrFromString("STATIC")
		textUTF, _ := syscall.UTF16PtrFromString(text)
		hLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(textUTF)),
			wsChild|wsVisible|ssLeft,
			uintptr(dpiScale(14)), uintptr(dpiScale(10)), uintptr(winW-dpiScale(30)), uintptr(labelH),
			hMainWnd, 0, 0, 0)

		btnY := dpiScale(18) + labelH
		btnClass, _ := syscall.UTF16PtrFromString("BUTTON")
		okText, _ := syscall.UTF16PtrFromString("OK")
		hOK, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(okText)),
			wsChild|wsVisible|wsTabStop|bsDefPushButton,
			uintptr(winW/2-dpiScale(40)), uintptr(btnY), uintptr(dpiScale(80)), uintptr(dpiScale(26)),
			hMainWnd, idOK, 0, 0)

		if hFont != 0 {
			pSendMessageW.Call(hLabel, wmSetFont, hFont, 1)
			pSendMessageW.Call(hOK, wmSetFont, hFont, 1)
		}

		screenW, _, _ := pGetSystemMetrics.Call(0)
		screenH, _, _ := pGetSystemMetrics.Call(1)
		pSetWindowPos.Call(hMainWnd, 0,
			(screenW-uintptr(winW))/2, (screenH-uintptr(winH))/2, 0, 0, 0x0001|0x0004)

		pShowWindow.Call(hMainWnd, swShow)
		pUpdateWindow.Call(hMainWnd)
		pSetForegroundWindow.Call(hMainWnd)

		type MSG struct {
			Hwnd    uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			Pt      [2]int32
		}
		var msg MSG
		for {
			ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if ret == 0 || ret == ^uintptr(0) {
				break
			}
			isDlg, _, _ := pIsDialogMessageW.Call(hMainWnd, uintptr(unsafe.Pointer(&msg)))
			if isDlg != 0 {
				continue
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()
}

// openFileInViewer opens a file with the default viewer.
func openFileInViewer(path string) {
	pathPtr, _ := syscall.UTF16PtrFromString(path)
	verb, _ := syscall.UTF16PtrFromString("open")
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(pathPtr)), 0, 0, 1)
}

// openURLInBrowser opens a URL in the default browser.
func openURLInBrowser(url string) {
	urlPtr, _ := syscall.UTF16PtrFromString(url)
	verb, _ := syscall.UTF16PtrFromString("open")
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(urlPtr)), 0, 0, 1)
}

const wmClose = 0x0010

func closeWindow(hwnd uintptr) {
	pPostMessageW.Call(hwnd, wmClose, 0, 0)
}

// --- Native Win32 Input Dialog ---

func inputBox(prompt, title string) string {
	return win32InputBox(prompt, title, false, "")
}

func inputBoxWithDefault(prompt, title, defaultVal string) string {
	return win32InputBox(prompt, title, false, defaultVal)
}

func inputBoxPassword(prompt, title string) string {
	return win32InputBox(prompt, title, true, "")
}

const (
	wsOverlapped    = 0x00000000
	wsCaption       = 0x00C00000
	wsSysMenu       = 0x00080000
	wsVisible       = 0x10000000
	wsChild         = 0x40000000
	wsTabStop       = 0x00010000
	wsBorder        = 0x00800000
	wsExTopmost     = 0x00000008
	esPassword      = 0x0020
	esAutoHScroll   = 0x0080
	ssLeft          = 0x00000000
	bsDefPushButton = 0x0001
	wmCommand       = 0x0111
	wmDestroy       = 0x0002
	wmSetFont       = 0x0030
	bnClicked       = 0
	swShow          = 5
	idEdit          = 100
	idOK            = 1    // IDOK — стандартный Windows ID, IsDialogMessage отправляет Enter как WM_COMMAND с IDOK=1
	idCancel        = 2    // IDCANCEL — стандартный Windows ID
	cwUseDefault    = 0x80000000
)

func win32InputBox(prompt, title string, isPassword bool, defaultVal string) string {
	var result string
	done := make(chan struct{})

	go func() {
		defer close(done)
		runtime.LockOSThread()

		var hEdit uintptr
		var hMainWnd uintptr

		// Шрифт.
		hFont := createScaledFont()

		// Уникальное имя класса (чтобы не конфликтовать при повторном вызове).
		classCounter++
		clsName := fmt.Sprintf("PCInputDlg_%d", classCounter)
		className, _ := syscall.UTF16PtrFromString(clsName)

		// Хелпер: прочитать текст из edit и закрыть окно.
		confirmAndClose := func() {
			buf := make([]uint16, 512)
			pGetWindowTextW.Call(hEdit, uintptr(unsafe.Pointer(&buf[0])), 512)
			result = syscall.UTF16ToString(buf)
			pDestroyWindow.Call(hMainWnd)
		}

		cancelAndClose := func() {
			result = ""
			pDestroyWindow.Call(hMainWnd)
		}

		wndProc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmCommand:
				id := wParam & 0xFFFF
				notif := (wParam >> 16) & 0xFFFF
				if notif == bnClicked || notif == 0 {
					if id == idOK {
						confirmAndClose()
						return 0
					} else if id == idCancel {
						cancelAndClose()
						return 0
					}
				}
			case wmClose:
				cancelAndClose()
				return 0
			case wmDestroy:
				pPostQuitMessage.Call(0)
				return 0
			}
			ret, _, _ := pDefWindowProcW.Call(hwnd, msg, wParam, lParam)
			return ret
		})

		type WNDCLASSEX struct {
			Size       uint32
			Style      uint32
			WndProc    uintptr
			ClsExtra   int32
			WndExtra   int32
			Instance   uintptr
			Icon       uintptr
			Cursor     uintptr
			Background uintptr
			MenuName   *uint16
			ClassName  *uint16
			IconSm     uintptr
		}

		wc := WNDCLASSEX{
			Size:       uint32(unsafe.Sizeof(WNDCLASSEX{})),
			WndProc:    wndProc,
			Background: 16, // COLOR_BTNFACE + 1
			ClassName:  className,
		}
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		// Размер окна — адаптивный по количеству строк prompt.
		promptLines := 1
		for _, c := range prompt {
			if c == '\n' {
				promptLines++
			}
		}
		inputWinW := dpiScale(340)
		inputWinH := dpiScale(150) + (promptLines-1)*dpiScale(22)

		titlePtr, _ := syscall.UTF16PtrFromString(title)
		hMainWnd, _, _ = pCreateWindowExW.Call(
			wsExTopmost,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(titlePtr)),
			wsOverlapped|wsCaption|wsSysMenu,
			uintptr(cwUseDefault), uintptr(cwUseDefault), uintptr(inputWinW), uintptr(inputWinH),
			0, 0, 0, 0)

		setWindowIcon(hMainWnd)

		// Label.
		labelH := dpiScale(22) * promptLines
		staticClass, _ := syscall.UTF16PtrFromString("STATIC")
		promptPtr, _ := syscall.UTF16PtrFromString(prompt)
		hLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(promptPtr)),
			wsChild|wsVisible|ssLeft,
			uintptr(dpiScale(14)), uintptr(dpiScale(10)), uintptr(inputWinW-dpiScale(30)), uintptr(labelH),
			hMainWnd, 0, 0, 0)

		// Edit.
		editY := dpiScale(14) + labelH
		editClass, _ := syscall.UTF16PtrFromString("EDIT")
		editStyle := uintptr(wsChild | wsVisible | wsBorder | wsTabStop | esAutoHScroll)
		if isPassword {
			editStyle |= esPassword
		}
		hEdit, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(editClass)), 0,
			editStyle,
			uintptr(dpiScale(14)), uintptr(editY), uintptr(inputWinW-dpiScale(30)), uintptr(dpiScale(24)),
			hMainWnd, idEdit, 0, 0)

		// Предзаполнение поля ввода.
		if defaultVal != "" {
			defPtr, _ := syscall.UTF16PtrFromString(defaultVal)
			pSendMessageW.Call(hEdit, 0x000C, 0, uintptr(unsafe.Pointer(defPtr)))
		}

		// OK button.
		btnY := editY + dpiScale(34)
		btnW := dpiScale(80)
		btnClass, _ := syscall.UTF16PtrFromString("BUTTON")
		okText, _ := syscall.UTF16PtrFromString("OK")
		hOK, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(okText)),
			wsChild|wsVisible|wsTabStop|bsDefPushButton,
			uintptr(inputWinW/2-btnW-dpiScale(4)), uintptr(btnY), uintptr(btnW), uintptr(dpiScale(26)),
			hMainWnd, idOK, 0, 0)

		// Cancel button.
		cancelBtnText, _ := syscall.UTF16PtrFromString(tr("pause.cancel"))
		hCancel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(cancelBtnText)),
			wsChild|wsVisible|wsTabStop,
			uintptr(inputWinW/2+dpiScale(4)), uintptr(btnY), uintptr(btnW), uintptr(dpiScale(26)),
			hMainWnd, idCancel, 0, 0)

		// Шрифт на все контролы.
		if hFont != 0 {
			for _, ctrl := range []uintptr{hLabel, hEdit, hOK, hCancel} {
				pSendMessageW.Call(ctrl, wmSetFont, hFont, 1)
			}
		}

		// Центрируем на экране.
		screenW, _, _ := pGetSystemMetrics.Call(0)
		screenH, _, _ := pGetSystemMetrics.Call(1)
		pSetWindowPos.Call(hMainWnd, 0,
			(screenW-uintptr(inputWinW))/2, (screenH-uintptr(inputWinH))/2, 0, 0,
			0x0001|0x0004)

		pShowWindow.Call(hMainWnd, swShow)
		pUpdateWindow.Call(hMainWnd)
		pSetForegroundWindow.Call(hMainWnd)
		pSetFocus.Call(hEdit)

		// Message loop с поддержкой Tab и Enter через IsDialogMessage.
		type MSG struct {
			Hwnd    uintptr
			Message uint32
			WParam  uintptr
			LParam  uintptr
			Time    uint32
			Pt      [2]int32
		}
		var msg MSG
		for {
			ret, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if ret == 0 || ret == ^uintptr(0) {
				break
			}
			// IsDialogMessage обрабатывает Tab и Enter (нажатие Enter
			// на кнопке BS_DEFPUSHBUTTON генерирует WM_COMMAND с idOK).
			isDlg, _, _ := pIsDialogMessageW.Call(hMainWnd, uintptr(unsafe.Pointer(&msg)))
			if isDlg != 0 {
				continue
			}
			pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}()

	<-done
	return strings.TrimSpace(result)
}

// classCounter для уникальных имён классов окон.
var classCounter int

// --- ICO generation ---

// putLE32 writes a little-endian uint32 into buf at offset off.
func putLE32(buf []byte, off int, v uint32) {
	buf[off] = byte(v)
	buf[off+1] = byte(v >> 8)
	buf[off+2] = byte(v >> 16)
	buf[off+3] = byte(v >> 24)
}

// putLE16 writes a little-endian uint16 into buf at offset off.
func putLE16(buf []byte, off int, v uint16) {
	buf[off] = byte(v)
	buf[off+1] = byte(v >> 8)
}

// generateHourglassICO creates a 16x16 32-bit BGRA ICO with an hourglass shape.
func generateHourglassICO() []byte {
	const (
		w       = 16
		h       = 16
		bpp     = 32
		imgSize = w * h * 4
		andSize = w * ((w + 31) / 32) * 4 * 0 // no AND mask for 32-bit
	)

	// ICO header: 6 bytes + 1 entry (16 bytes) = 22 bytes.
	// BMP info header: 40 bytes.
	// Pixel data: imgSize bytes.
	headerSize := 6 + 16
	bmpHeaderSize := 40
	totalSize := headerSize + bmpHeaderSize + imgSize

	ico := make([]byte, totalSize)

	// ICO header.
	putLE16(ico, 0, 0)      // reserved
	putLE16(ico, 2, 1)      // type: icon
	putLE16(ico, 4, 1)      // count: 1
	ico[6] = byte(w)        // width
	ico[7] = byte(h)        // height
	ico[8] = 0              // color count
	ico[9] = 0              // reserved
	putLE16(ico, 10, 1)     // planes
	putLE16(ico, 12, bpp)   // bits per pixel
	putLE32(ico, 14, uint32(bmpHeaderSize+imgSize)) // data size
	putLE32(ico, 18, uint32(headerSize))            // data offset

	// BMP info header.
	off := headerSize
	putLE32(ico, off, uint32(bmpHeaderSize)) // header size
	putLE32(ico, off+4, w)                   // width
	putLE32(ico, off+8, h*2)                 // height (doubled for ICO)
	putLE16(ico, off+12, 1)                  // planes
	putLE16(ico, off+14, bpp)                // bits per pixel
	// rest is zeros (no compression, etc.)

	// Pixel data (bottom-up BGRA).
	pixOff := headerSize + bmpHeaderSize

	// Colors.
	bg := [4]byte{0, 0, 0, 0}           // transparent
	sand := [4]byte{130, 200, 220, 255}  // light sand (BGRA)
	frame := [4]byte{140, 120, 80, 255}  // dark frame (BGRA)
	glass := [4]byte{180, 180, 100, 255} // glass body (BGRA)

	_ = bg
	_ = sand

	// 16x16 hourglass pattern (row 0 = bottom of image).
	pattern := [16][16]byte{
		//0  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0}, // row 0 (bottom)
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0}, // row 1
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0}, // row 2
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0}, // row 3
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0}, // row 4
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0}, // row 5
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0}, // row 6
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0}, // row 7
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0}, // row 8
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0}, // row 9
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0}, // row 10
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0}, // row 11
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0}, // row 12
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0}, // row 13
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0}, // row 14
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0}, // row 15 (top)
	}

	colors := [4][4]byte{bg, frame, glass, sand}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := pattern[y][x]
			c := colors[idx]
			p := pixOff + (y*w+x)*4
			ico[p] = c[0]
			ico[p+1] = c[1]
			ico[p+2] = c[2]
			ico[p+3] = c[3]
		}
	}

	return ico
}

// generatePausedICO creates the same hourglass but with a thick red horizontal bar.
func generatePausedICO() []byte {
	ico := generateHourglassICO()

	// Pixel data starts after ICO header (22) + BMP header (40) = offset 62.
	const (
		w      = 16
		pixOff = 62
	)

	red := [4]byte{0, 0, 255, 255} // BGRA red

	// Draw a 4px-thick horizontal red bar across the center (rows 6-9).
	for y := 6; y <= 9; y++ {
		for x := 1; x < w-1; x++ {
			p := pixOff + (y*w+x)*4
			ico[p] = red[0]
			ico[p+1] = red[1]
			ico[p+2] = red[2]
			ico[p+3] = red[3]
		}
	}

	return ico
}

// generateGreenICO creates the hourglass with green tint (filter paused / entertainment allowed).
func generateGreenICO() []byte {
	const (
		w      = 16
		h      = 16
		bpp    = 32
		imgSize = w * h * 4
	)

	headerSize := 6 + 16
	bmpHeaderSize := 40
	totalSize := headerSize + bmpHeaderSize + imgSize

	ico := make([]byte, totalSize)

	putLE16(ico, 0, 0)
	putLE16(ico, 2, 1)
	putLE16(ico, 4, 1)
	ico[6] = byte(w)
	ico[7] = byte(h)
	putLE16(ico, 10, 1)
	putLE16(ico, 12, bpp)
	putLE32(ico, 14, uint32(bmpHeaderSize+imgSize))
	putLE32(ico, 18, uint32(headerSize))

	off := headerSize
	putLE32(ico, off, uint32(bmpHeaderSize))
	putLE32(ico, off+4, w)
	putLE32(ico, off+8, h*2)
	putLE16(ico, off+12, 1)
	putLE16(ico, off+14, bpp)

	pixOff := headerSize + bmpHeaderSize

	bg := [4]byte{0, 0, 0, 0}
	frame := [4]byte{80, 160, 80, 255}    // green frame (BGRA)
	glass := [4]byte{100, 200, 100, 255}   // green glass
	sand := [4]byte{130, 230, 130, 255}    // light green sand

	pattern := [16][16]byte{
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0},
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
	}

	colors := [4][4]byte{bg, frame, glass, sand}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := pattern[y][x]
			c := colors[idx]
			p := pixOff + (y*w+x)*4
			ico[p] = c[0]
			ico[p+1] = c[1]
			ico[p+2] = c[2]
			ico[p+3] = c[3]
		}
	}

	return ico
}

// generateLearningICO creates the hourglass with a green "?" overlay (learning mode).
func generateLearningICO() []byte {
	ico := generateHourglassICO()

	const (
		w      = 16
		pixOff = 62
	)

	red := [4]byte{0, 0, 255, 255}       // BGRA bright red
	dark := [4]byte{0, 0, 160, 255}      // BGRA dark red outline

	// Big "?" covering most of the 16x16 icon (rows 1-14).
	// Outline (dark red).
	for _, pt := range [][2]int{
		{4, 14}, {11, 14},
		{4, 13}, {11, 13},
		{3, 12}, {4, 12}, {11, 12}, {12, 12},
		{3, 11}, {12, 11},
		{12, 10},
		{11, 9}, {12, 9},
		{10, 8}, {11, 8},
		{8, 7}, {9, 7}, {10, 7},
		{7, 6}, {8, 6},
		{7, 5},
		{7, 3}, {8, 3},
		{7, 2}, {10, 2},
		{10, 1},
	} {
		x, y := pt[0], pt[1]
		p := pixOff + (y*w+x)*4
		ico[p], ico[p+1], ico[p+2], ico[p+3] = dark[0], dark[1], dark[2], dark[3]
	}

	// Fill (bright red).
	for _, pt := range [][2]int{
		// Top arc.
		{5, 14}, {6, 14}, {7, 14}, {8, 14}, {9, 14}, {10, 14},
		{5, 13}, {6, 13}, {7, 13}, {8, 13}, {9, 13}, {10, 13},
		{5, 12}, {6, 12}, {7, 12}, {8, 12}, {9, 12}, {10, 12},
		{4, 11}, {5, 11}, {6, 11}, {7, 11}, {8, 11}, {9, 11}, {10, 11}, {11, 11},
		// Right side going down.
		{10, 10}, {11, 10},
		{9, 9}, {10, 9},
		// Stem.
		{8, 8}, {9, 8},
		{8, 7}, {9, 7},
		{8, 6},
		{8, 5},
		// Dot.
		{8, 2}, {9, 2},
		{8, 1}, {9, 1},
	} {
		x, y := pt[0], pt[1]
		p := pixOff + (y*w+x)*4
		ico[p], ico[p+1], ico[p+2], ico[p+3] = red[0], red[1], red[2], red[3]
	}

	return ico
}

// generateUnrestrictedICO creates orange hourglass (unrestricted mode).
func generateUnrestrictedICO() []byte {
	const (
		w       = 16
		h       = 16
		bpp     = 32
		imgSize = w * h * 4
	)

	headerSize := 6 + 16
	bmpHeaderSize := 40
	totalSize := headerSize + bmpHeaderSize + imgSize
	ico := make([]byte, totalSize)

	putLE16(ico, 0, 0)
	putLE16(ico, 2, 1)
	putLE16(ico, 4, 1)
	ico[6] = byte(w)
	ico[7] = byte(h)
	putLE16(ico, 10, 1)
	putLE16(ico, 12, bpp)
	putLE32(ico, 14, uint32(bmpHeaderSize+imgSize))
	putLE32(ico, 18, uint32(headerSize))

	off := headerSize
	putLE32(ico, off, uint32(bmpHeaderSize))
	putLE32(ico, off+4, w)
	putLE32(ico, off+8, h*2)
	putLE16(ico, off+12, 1)
	putLE16(ico, off+14, bpp)

	pixOff := headerSize + bmpHeaderSize

	bg := [4]byte{0, 0, 0, 0}
	frame := [4]byte{60, 120, 200, 255}    // orange frame (BGRA)
	glass := [4]byte{80, 160, 240, 255}    // orange glass
	sand := [4]byte{100, 190, 255, 255}    // light orange sand

	pattern := [16][16]byte{
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0},
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 3, 3, 3, 3, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 3, 3, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 0, 1, 2, 2, 1, 0, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 0, 1, 2, 2, 2, 2, 1, 0, 0, 0, 0, 0},
		{0, 0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0, 0},
		{0, 0, 0, 1, 2, 2, 2, 2, 2, 2, 2, 2, 1, 0, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
		{0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 0},
	}

	colors := [4][4]byte{bg, frame, glass, sand}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := pattern[y][x]
			c := colors[idx]
			p := pixOff + (y*w+x)*4
			ico[p], ico[p+1], ico[p+2], ico[p+3] = c[0], c[1], c[2], c[3]
		}
	}
	return ico
}

// hourglassICO — иконка песочных часов (обычный режим).
var hourglassICO = generateHourglassICO()

// hourglassPausedICO — иконка песочных часов с красной полосой (пауза развлечений).
var hourglassPausedICO = generatePausedICO()

// hourglassGreenICO — зелёные песочные часы (развлечения разрешены / фильтрация приостановлена).
var hourglassGreenICO = generateGreenICO()

// hourglassLearningICO — песочные часы с зелёным вопросительным знаком (режим обучения).
var hourglassLearningICO = generateLearningICO()

// hourglassUnrestrictedICO — оранжевые песочные часы (режим без ограничений).
var hourglassUnrestrictedICO = generateUnrestrictedICO()
