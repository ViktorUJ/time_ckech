package monitor

import (
	"fmt"
	"strings"
	"testing"

	"parental-control-service/internal/config"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 2: Классификация системных процессов
// **Validates: Requirements 3.1, 3.2**

// mockSignatureChecker — мок для проверки подписи Microsoft.
type mockSignatureChecker struct {
	result bool
}

func (m *mockSignatureChecker) IsMicrosoftSigned(exePath string) bool {
	return m.result
}

// genWindowsSubpath генерирует случайный подпуть с каталогами и именем файла .exe.
func genWindowsSubpath() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		depth := rapid.IntRange(1, 5).Draw(t, "depth")
		path := ""
		for i := 0; i < depth; i++ {
			dirName := rapid.StringMatching(`[A-Za-z][A-Za-z0-9_\-]{0,15}`).Draw(t, fmt.Sprintf("dir_%d", i))
			path += dirName + `\`
		}
		fileName := rapid.StringMatching(`[A-Za-z][A-Za-z0-9_\-]{0,10}\.exe`).Draw(t, "fileName")
		return path + fileName
	})
}

func TestPropertySystemProcessClassificationByPath(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Выбираем один из системных префиксов
		prefixes := []string{
			`C:\Windows\`,
			`C:\Program Files\`,
			`C:\Program Files (x86)\`,
			`C:\ProgramData\`,
		}
		prefixIdx := rapid.IntRange(0, len(prefixes)-1).Draw(t, "prefixIdx")
		prefix := prefixes[prefixIdx]

		subpath := genWindowsSubpath().Draw(t, "subpath")
		exePath := prefix + subpath

		classifier := NewDefaultClassifier(nil, &mockSignatureChecker{result: false}, nil)

		pid := rapid.Uint32().Draw(t, "pid")
		name := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{0,10}\.exe`).Draw(t, "name")

		info := classifier.Classify(pid, name, exePath)

		if !info.IsSystem {
			t.Fatalf("expected IsSystem=true for path %q, got IsSystem=false", exePath)
		}
	})
}

func TestPropertySystemProcessClassificationBySignature(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Генерируем произвольный НЕсистемный путь (не начинается с системных префиксов)
		dirName := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{2,10}`).Draw(t, "dirName")
		subpath := genWindowsSubpath().Draw(t, "subpath")
		exePath := `C:\Users\` + dirName + `\` + subpath

		// Мок возвращает true — процесс подписан Microsoft
		classifier := NewDefaultClassifier(nil, &mockSignatureChecker{result: true}, nil)

		pid := rapid.Uint32().Draw(t, "pid")
		name := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{0,10}\.exe`).Draw(t, "name")

		info := classifier.Classify(pid, name, exePath)

		if !info.IsSystem {
			t.Fatalf("expected IsSystem=true for Microsoft-signed path %q, got IsSystem=false", exePath)
		}
	})
}

// Feature: parental-control-service, Property 3: Классификация процессов по списку разрешённых
// **Validates: Requirements 4.2, 4.3**

func TestPropertyAllowedProcessClassification(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Генерируем случайное имя исполняемого файла
		exeName := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{1,10}\.exe`).Draw(t, "exeName")

		// Генерируем AllowedApp с опциональным путём
		appName := rapid.StringMatching(`[A-Za-z][A-Za-z0-9 ]{2,15}`).Draw(t, "appName")
		hasPath := rapid.Bool().Draw(t, "hasPath")

		// Генерируем не-системный путь для процесса
		userDirs := []string{`C:\Users\`, `D:\Games\`, `E:\Apps\`}
		dirIdx := rapid.IntRange(0, len(userDirs)-1).Draw(t, "dirIdx")
		baseDir := userDirs[dirIdx]
		subDir := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{1,10}`).Draw(t, "subDir")
		processPath := baseDir + subDir + `\` + exeName

		var allowedPath string
		if hasPath {
			// Путь в AllowedApp совпадает с путём процесса
			allowedPath = processPath
		}

		allowedApp := config.AllowedApp{
			Name:       appName,
			Executable: exeName,
			Path:       allowedPath,
		}

		// Случайный регистр имени процесса для проверки регистронезависимости
		casedName := rapid.SampledFrom([]string{
			strings.ToLower(exeName),
			strings.ToUpper(exeName),
			exeName,
		}).Draw(t, "casedName")

		classifier := NewDefaultClassifier([]config.AllowedApp{allowedApp}, &mockSignatureChecker{result: false}, nil)

		pid := rapid.Uint32().Draw(t, "pid")
		info := classifier.Classify(pid, casedName, processPath)

		if !info.IsAllowed {
			t.Fatalf("expected IsAllowed=true for process %q (exe=%q, path=%q) with allowed app %+v, got IsAllowed=false",
				casedName, exeName, processPath, allowedApp)
		}
	})
}

func TestPropertyRestrictedProcessClassification(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Генерируем список разрешённых приложений (0-5 записей)
		numApps := rapid.IntRange(0, 5).Draw(t, "numApps")
		allowedApps := make([]config.AllowedApp, numApps)
		for i := 0; i < numApps; i++ {
			allowedApps[i] = config.AllowedApp{
				Name:       rapid.StringMatching(`[A-Za-z][A-Za-z0-9 ]{2,15}`).Draw(t, fmt.Sprintf("appName_%d", i)),
				Executable: rapid.StringMatching(`[a-z]{3,10}\.exe`).Draw(t, fmt.Sprintf("appExe_%d", i)),
			}
		}

		// Генерируем имя процесса, которое гарантированно НЕ совпадает ни с одним разрешённым
		processExe := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{1,10}\.exe`).Draw(t, "processExe")
		lowerProcessExe := strings.ToLower(processExe)
		for _, app := range allowedApps {
			if strings.EqualFold(lowerProcessExe, strings.ToLower(app.Executable)) {
				// Добавляем уникальный суффикс, чтобы гарантировать несовпадение
				processExe = "zz_" + processExe
				break
			}
		}

		// Не-системный путь
		userDirs := []string{`C:\Users\`, `D:\Games\`, `E:\Apps\`}
		dirIdx := rapid.IntRange(0, len(userDirs)-1).Draw(t, "dirIdx")
		baseDir := userDirs[dirIdx]
		subDir := rapid.StringMatching(`[A-Za-z][A-Za-z0-9]{1,10}`).Draw(t, "subDir")
		processPath := baseDir + subDir + `\` + processExe

		classifier := NewDefaultClassifier(allowedApps, &mockSignatureChecker{result: false}, nil)

		pid := rapid.Uint32().Draw(t, "pid")
		info := classifier.Classify(pid, processExe, processPath)

		if info.IsAllowed {
			t.Fatalf("expected IsAllowed=false for process %q (path=%q) not in allowed list %+v, got IsAllowed=true",
				processExe, processPath, allowedApps)
		}
		if info.IsSystem {
			t.Fatalf("expected IsSystem=false for process %q (path=%q), got IsSystem=true",
				processExe, processPath)
		}
	})
}
