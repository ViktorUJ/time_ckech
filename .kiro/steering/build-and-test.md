---
inclusion: auto
---

# Сборка и тестирование

## Сборка

```bash
# Сборка Windows-службы
GOOS=windows GOARCH=amd64 go build -o parental-control-service.exe ./cmd/service/

# Сборка утилиты установки
GOOS=windows GOARCH=amd64 go build -o installer.exe ./cmd/installer/
```

## Тестирование

```bash
# Запуск всех тестов
go test ./...

# Запуск тестов с подробным выводом
go test -v ./...

# Запуск тестов конкретного пакета
go test -v ./internal/scheduler/

# Запуск property-based тестов
go test -v -run TestProperty ./...
```

## Установка службы (на целевой машине)

```powershell
# Установка службы (от имени администратора)
.\installer.exe -install

# Удаление службы
.\installer.exe -remove

# Запуск службы
sc start ParentalControlService

# Остановка службы
sc stop ParentalControlService
```

## Проверка логов

```powershell
# Просмотр логов службы в Event Viewer
Get-EventLog -LogName Application -Source "ParentalControlService" -Newest 20
```

## Зависимости

```bash
# Инициализация модуля
go mod init parental-control-service

# Основные зависимости
go get golang.org/x/sys/windows
go get golang.org/x/sys/windows/svc
```
