//go:build windows

package main

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32i   = syscall.NewLazyDLL("user32.dll")
	kernel32i = syscall.NewLazyDLL("kernel32.dll")
	gdi32i    = syscall.NewLazyDLL("gdi32.dll")

	pRegisterClassExW = user32i.NewProc("RegisterClassExW")
	pCreateWindowExW  = user32i.NewProc("CreateWindowExW")
	pDefWindowProcW   = user32i.NewProc("DefWindowProcW")
	pGetMessageW      = user32i.NewProc("GetMessageW")
	pTranslateMessage = user32i.NewProc("TranslateMessage")
	pDispatchMessageW = user32i.NewProc("DispatchMessageW")
	pDestroyWindow    = user32i.NewProc("DestroyWindow")
	pPostQuitMessage  = user32i.NewProc("PostQuitMessage")
	pSendMessageW     = user32i.NewProc("SendMessageW")
	pSetFocus         = user32i.NewProc("SetFocus")
	pIsDialogMessageW = user32i.NewProc("IsDialogMessageW")
	pShowWindow       = user32i.NewProc("ShowWindow")
	pUpdateWindow     = user32i.NewProc("UpdateWindow")
	pGetSystemMetrics = user32i.NewProc("GetSystemMetrics")
	pSetWindowPos     = user32i.NewProc("SetWindowPos")
	pCreateFontW      = gdi32i.NewProc("CreateFontW")
	pMessageBoxW      = user32i.NewProc("MessageBoxW")
	pGetWindowTextW   = user32i.NewProc("GetWindowTextW")
	pEnableWindow     = user32i.NewProc("EnableWindow")
	pSetWindowTextW   = user32i.NewProc("SetWindowTextW")
	pInvalidateRect   = user32i.NewProc("InvalidateRect")
)

const (
	wsOverlapped    = 0x00000000
	wsCaption       = 0x00C00000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	wsVisible       = 0x10000000
	wsChild         = 0x40000000
	wsTabStop       = 0x00010000
	wsBorder        = 0x00800000
	wsVScroll       = 0x00200000
	esPassword      = 0x0020
	esAutoHScroll   = 0x0080
	esMultiline     = 0x0004
	esReadOnly      = 0x0800
	ssLeft          = 0x00000000
	bsDefPushButton = 0x0001
	bsGroupBox      = 0x0007
	wmCommand       = 0x0111
	wmClose         = 0x0010
	wmDestroy       = 0x0002
	wmSetFont       = 0x0030
	wmCtlColorStatic = 0x0138
	wmCtlColorEdit   = 0x0133
	bnClicked       = 0
	swShow          = 5
	cwUseDefault    = 0x80000000
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbIconInfo      = 0x00000040
	mbYesNo         = 0x00000004
	mbIconQuestion  = 0x00000020
	idYes           = 6
)

// InstallerParams — результат GUI-диалога.
type InstallerParams struct {
	ConfigURL string
	Password  string
	Cancelled bool
}


func showInstallerGUI() InstallerParams {
	var result InstallerParams
	result.Cancelled = true
	done := make(chan struct{})

	go func() {
		defer close(done)
		runtime.LockOSThread()

		// Включаем DPI awareness для чёткого рендеринга.
		if proc, err := syscall.LoadDLL("user32.dll"); err == nil {
			if p, err := proc.FindProc("SetProcessDPIAware"); err == nil {
				p.Call()
			}
		}

		const (
			winW = 640
			winH = 500
		)

		// IDs.
		const (
			idURLEdit     = 100
			idPwdEdit     = 101
			idPwdConfirm  = 102
			idInstallBtn  = 103
			idCancelBtn   = 104
			idUninstallBtn = 105
			idStatusLabel = 106
			idOK          = 1
			idCancel      = 2
		)

		var hMainWnd, hURLEdit, hPwdEdit, hPwdConfirm, hStatusLabel, hInstallBtn, hCancelBtn uintptr

		// Шрифт Segoe UI.
		fontName, _ := syscall.UTF16PtrFromString("Segoe UI")
		hFont, _, _ := pCreateFontW.Call(
			uintptr(uint32(0xFFFFFFF0)), // -16 ≈ 11pt
			0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0,
			uintptr(unsafe.Pointer(fontName)))

		hFontTitle, _, _ := pCreateFontW.Call(
			uintptr(uint32(0xFFFFFFEA)), // -22 ≈ 15pt
			0, 0, 0, 700, 0, 0, 0, 1, 0, 0, 5, 0,
			uintptr(unsafe.Pointer(fontName)))

		hFontSmall, _, _ := pCreateFontW.Call(
			uintptr(uint32(0xFFFFFFF3)), // -13 ≈ 9pt
			0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0,
			uintptr(unsafe.Pointer(fontName)))

		className, _ := syscall.UTF16PtrFromString("PCInstallerDlg")

		getText := func(hwnd uintptr) string {
			buf := make([]uint16, 1024)
			pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 1024)
			return strings.TrimSpace(syscall.UTF16ToString(buf))
		}

		setStatus := func(text string) {
			ptr, _ := syscall.UTF16PtrFromString(text)
			pSetWindowTextW.Call(hStatusLabel, uintptr(unsafe.Pointer(ptr)))
		}

		wndProc := syscall.NewCallback(func(hwnd, msg, wParam, lParam uintptr) uintptr {
			switch msg {
			case wmCommand:
				id := wParam & 0xFFFF
				notif := (wParam >> 16) & 0xFFFF
				if notif == bnClicked || notif == 0 {
					switch id {
					case idInstallBtn, idOK:
						url := getText(hURLEdit)
						pwd := getText(hPwdEdit)
						confirm := getText(hPwdConfirm)

						if url == "" {
							msgBox(hwnd, "Укажите URL конфигурации", "Ошибка", mbOK|mbIconError)
							return 0
						}
						if pwd == "" {
							msgBox(hwnd, "Укажите пароль", "Ошибка", mbOK|mbIconError)
							return 0
						}
						if pwd != confirm {
							msgBox(hwnd, "Пароли не совпадают", "Ошибка", mbOK|mbIconError)
							return 0
						}
						if len(pwd) < 1 {
							msgBox(hwnd, "Пароль не может быть пустым", "Ошибка", mbOK|mbIconError)
							return 0
						}

						result.ConfigURL = url
						result.Password = pwd
						result.Cancelled = false
						pDestroyWindow.Call(hMainWnd)
						return 0

					case idCancelBtn, idCancel:
						result.Cancelled = true
						pDestroyWindow.Call(hMainWnd)
						return 0

					case idUninstallBtn:
						ret := msgBoxYesNo(hwnd, "Удалить сервис родительского контроля?\n\nДанные (state.json, статистика) будут сохранены.", "Удаление")
						if ret == idYes {
							result.Cancelled = true
							// Выполняем деинсталляцию прямо здесь.
							setStatus("Удаление...")
							pEnableWindow.Call(hInstallBtn, 0)
							pEnableWindow.Call(hCancelBtn, 0)
							go func() {
								err := doUninstall(false)
								if err != nil {
									msgBox(hwnd, fmt.Sprintf("Ошибка удаления: %v", err), "Ошибка", mbOK|mbIconError)
								} else {
									msgBox(hwnd, "Сервис успешно удалён", "Готово", mbOK|mbIconInfo)
								}
								pDestroyWindow.Call(hMainWnd)
							}()
						}
						return 0
					}
				}
			case wmClose:
				result.Cancelled = true
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
			Background: 16, // COLOR_BTNFACE + 1
			ClassName:  className,
		}
		pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

		titlePtr, _ := syscall.UTF16PtrFromString("Parental Control — Установка")
		hMainWnd, _, _ = pCreateWindowExW.Call(
			0,
			uintptr(unsafe.Pointer(className)),
			uintptr(unsafe.Pointer(titlePtr)),
			wsOverlapped|wsCaption|wsSysMenu|wsMinimizeBox,
			uintptr(cwUseDefault), uintptr(cwUseDefault), winW, winH,
			0, 0, 0, 0)

		staticClass, _ := syscall.UTF16PtrFromString("STATIC")
		editClass, _ := syscall.UTF16PtrFromString("EDIT")
		btnClass, _ := syscall.UTF16PtrFromString("BUTTON")

		// Ширина контента.
		const pad = uintptr(24)
		const contentW = uintptr(winW - 24*2 - 16) // учёт рамки окна

		// Заголовок.
		titleText, _ := syscall.UTF16PtrFromString("Parental Control Service")
		hTitle, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(titleText)),
			wsChild|wsVisible|ssLeft,
			pad, 16, contentW, 28,
			hMainWnd, 0, 0, 0)

		// Описание.
		descText, _ := syscall.UTF16PtrFromString("Установка сервиса родительского контроля")
		hDesc, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(descText)),
			wsChild|wsVisible|ssLeft,
			pad, 48, contentW, 20,
			hMainWnd, 0, 0, 0)

		// Разделитель.
		sepText, _ := syscall.UTF16PtrFromString("")
		pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(sepText)),
			wsChild|wsVisible|0x00000010, // SS_ETCHEDHORZ
			pad, 76, contentW, 2,
			hMainWnd, 0, 0, 0)

		// --- Config URL ---
		urlLabel, _ := syscall.UTF16PtrFromString("URL конфигурации (Google Drive или GitHub):")
		hURLLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(urlLabel)),
			wsChild|wsVisible|ssLeft,
			pad, 90, contentW, 20,
			hMainWnd, 0, 0, 0)

		defaultURL, _ := syscall.UTF16PtrFromString("https://drive.google.com/uc?export=download&id=1fQ4Rkg_myHQnzOa9hfvopoTm8BgybOBg")
		hURLEdit, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(editClass)),
			uintptr(unsafe.Pointer(defaultURL)),
			wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll,
			pad, 114, contentW, 28,
			hMainWnd, idURLEdit, 0, 0)

		// --- Password ---
		pwdLabel, _ := syscall.UTF16PtrFromString("Пароль для управления (пауза, настройки):")
		hPwdLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(pwdLabel)),
			wsChild|wsVisible|ssLeft,
			pad, 160, contentW, 20,
			hMainWnd, 0, 0, 0)

		pwdFieldW := uintptr((contentW - 20) / 2)
		hPwdEdit, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(editClass)), 0,
			wsChild|wsVisible|wsBorder|wsTabStop|esPassword|esAutoHScroll,
			pad, 184, pwdFieldW, 28,
			hMainWnd, idPwdEdit, 0, 0)

		// --- Confirm password ---
		confirmLabel, _ := syscall.UTF16PtrFromString("Подтвердите пароль:")
		hConfirmLabel, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(confirmLabel)),
			wsChild|wsVisible|ssLeft,
			pad+pwdFieldW+20, 160, pwdFieldW, 20,
			hMainWnd, 0, 0, 0)

		hPwdConfirm, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(editClass)), 0,
			wsChild|wsVisible|wsBorder|wsTabStop|esPassword|esAutoHScroll,
			pad+pwdFieldW+20, 184, pwdFieldW, 28,
			hMainWnd, idPwdConfirm, 0, 0)

		// --- Status label ---
		hStatusLabel, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)), 0,
			wsChild|wsVisible|ssLeft,
			pad, 230, contentW, 20,
			hMainWnd, idStatusLabel, 0, 0)

		// --- Buttons ---
		const btnH = 36
		const btnY = 268

		installText, _ := syscall.UTF16PtrFromString("Установить")
		hInstallBtn, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(installText)),
			wsChild|wsVisible|wsTabStop|bsDefPushButton,
			pad, btnY, 150, btnH,
			hMainWnd, idInstallBtn, 0, 0)

		uninstallText, _ := syscall.UTF16PtrFromString("Удалить")
		hUninstallBtn, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(uninstallText)),
			wsChild|wsVisible|wsTabStop,
			pad+170, btnY, 130, btnH,
			hMainWnd, idUninstallBtn, 0, 0)

		cancelText, _ := syscall.UTF16PtrFromString("Отмена")
		hCancelBtn, _, _ = pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(btnClass)),
			uintptr(unsafe.Pointer(cancelText)),
			wsChild|wsVisible|wsTabStop,
			pad+contentW-120, btnY, 120, btnH,
			hMainWnd, idCancelBtn, 0, 0)

		// --- Info text ---
		infoText, _ := syscall.UTF16PtrFromString("Установка: C:\\Program Files\\ParentalControlService\\    Данные: C:\\ProgramData\\ParentalControlService\\")
		hInfo, _, _ := pCreateWindowExW.Call(0,
			uintptr(unsafe.Pointer(staticClass)),
			uintptr(unsafe.Pointer(infoText)),
			wsChild|wsVisible|ssLeft,
			pad, 330, contentW, 40,
			hMainWnd, 0, 0, 0)

		// Шрифты.
		allControls := []uintptr{hURLLabel, hURLEdit, hPwdLabel, hPwdEdit,
			hConfirmLabel, hPwdConfirm, hStatusLabel,
			hInstallBtn, hCancelBtn, hUninstallBtn, hDesc}
		for _, ctrl := range allControls {
			if ctrl != 0 && hFont != 0 {
				pSendMessageW.Call(ctrl, wmSetFont, hFont, 1)
			}
		}
		if hTitle != 0 && hFontTitle != 0 {
			pSendMessageW.Call(hTitle, wmSetFont, hFontTitle, 1)
		}
		if hInfo != 0 && hFontSmall != 0 {
			pSendMessageW.Call(hInfo, wmSetFont, hFontSmall, 1)
		}

		// Центрируем.
		screenW, _, _ := pGetSystemMetrics.Call(0) // SM_CXSCREEN
		screenH, _, _ := pGetSystemMetrics.Call(1) // SM_CYSCREEN
		pSetWindowPos.Call(hMainWnd, 0,
			(screenW-winW)/2, (screenH-winH)/2, 0, 0,
			0x0001|0x0004) // SWP_NOSIZE | SWP_NOZORDER

		pShowWindow.Call(hMainWnd, swShow)
		pUpdateWindow.Call(hMainWnd)
		pSetFocus.Call(hURLEdit)

		// Message loop.
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

	<-done
	return result
}

func msgBox(owner uintptr, text, title string, flags uintptr) int {
	titlePtr, _ := syscall.UTF16PtrFromString(title)
	textPtr, _ := syscall.UTF16PtrFromString(text)
	ret, _, _ := pMessageBoxW.Call(owner, uintptr(unsafe.Pointer(textPtr)), uintptr(unsafe.Pointer(titlePtr)), flags)
	return int(ret)
}

func msgBoxYesNo(owner uintptr, text, title string) int {
	return msgBox(owner, text, title, mbYesNo|mbIconQuestion)
}
