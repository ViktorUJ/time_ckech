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
	pIsDialogMessageW           = user32w.NewProc("IsDialogMessageW")
	pShowWindow                 = user32w.NewProc("ShowWindow")
	pUpdateWindow               = user32w.NewProc("UpdateWindow")
	pGetSystemMetrics           = user32w.NewProc("GetSystemMetrics")
	pSetWindowPos               = user32w.NewProc("SetWindowPos")
	pCreateFontW                = gdi32w.NewProc("CreateFontW")
)

const mutexName = "Global\\ParentalControlTray_SingleInstance"

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
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), 0x00000040)
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
	return win32InputBox(prompt, title, false)
}

func inputBoxPassword(prompt, title string) string {
	return win32InputBox(prompt, title, true)
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

func win32InputBox(prompt, title string, isPassword bool) string {
	var result string
	done := make(chan struct{})

	go func() {
		defer close(done)
		runtime.LockOSThread()

		var hEdit uintptr
		var hMainWnd uintptr

		// Шрифт Segoe UI 14pt.
		fontName, _ := syscall.UTF16PtrFromString("Segoe UI")
		hFont, _, _ := pCreateFontW.Call(
			uintptr(uint32(0xFFFFFFEA)), // -22 logical units ≈ 14pt
			0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0,
			uintptr(unsafe.Pointer(fontName)))

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

		// Размер окна: 420x200 (с учётом заголовка ~30px и рамки).
		const winW, winH = 420, 200

		titlePtr, _ := syscall.UTF16PtrFromString(title)
		hMainWnd, _, _ = pCreateWindowExW.Call(
			wsExTopmost,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(titlePtr)),
			wsOverlapped|wsCaption|wsSysMenu,
			uintptr(cwUseDefault), uintptr(cwUseDefault), winW, winH,
			0, 0, 0, 0)

		// Label.
		staticClass, _ := syscall.UTF16PtrFromString("STATIC")
		promptPtr, _ := syscall.UTF16PtrFromString(prompt)
		hLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(promptPtr)),
			wsChild|wsVisible|ssLeft,
			20, 15, 370, 24,
			hMainWnd, 0, 0, 0)

		// Edit.
		editClass, _ := syscall.UTF16PtrFromString("EDIT")
		editStyle := uintptr(wsChild | wsVisible | wsBorder | wsTabStop | esAutoHScroll)
		if isPassword {
			editStyle |= esPassword
		}
		hEdit, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(editClass)), 0,
			editStyle,
			20, 48, 370, 28,
			hMainWnd, idEdit, 0, 0)

		// OK button.
		btnClass, _ := syscall.UTF16PtrFromString("BUTTON")
		okText, _ := syscall.UTF16PtrFromString("OK")
		hOK, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(okText)),
			wsChild|wsVisible|wsTabStop|bsDefPushButton,
			200, 95, 90, 32,
			hMainWnd, idOK, 0, 0)

		// Cancel button.
		cancelBtnText, _ := syscall.UTF16PtrFromString(tr("pause.cancel"))
		hCancel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(cancelBtnText)),
			wsChild|wsVisible|wsTabStop,
			300, 95, 90, 32,
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
			(screenW-winW)/2, (screenH-winH)/2, 0, 0,
			0x0001|0x0004) // SWP_NOSIZE | SWP_NOZORDER

		pShowWindow.Call(hMainWnd, swShow)
		pUpdateWindow.Call(hMainWnd)
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

// hourglassICO — иконка песочных часов (генерируется при запуске).
var hourglassICO = generateHourglassICO()

// hourglassPausedICO — иконка песочных часов с красной полосой (пауза).
var hourglassPausedICO = generatePausedICO()
