---
inclusion: manual
---

# Формат конфигурационных файлов

## allowed_apps.json — Список разрешённых программ

```json
{
  "apps": [
    {
      "name": "Visual Studio Code",
      "executable": "Code.exe",
      "path": "C:\\Users\\*\\AppData\\Local\\Programs\\Microsoft VS Code\\Code.exe"
    },
    {
      "name": "Microsoft Word",
      "executable": "WINWORD.EXE"
    }
  ]
}
```

Поля:
- `name` — человекочитаемое имя (для логов)
- `executable` — имя исполняемого файла (обязательное, регистронезависимое)
- `path` — полный путь (опциональный, поддерживает wildcard `*`)

## allowed_sites.json — Список разрешённых сайтов

```json
{
  "sites": [
    {
      "domain": "google.com",
      "include_subdomains": true
    },
    {
      "domain": "wikipedia.org",
      "include_subdomains": true
    },
    {
      "domain": "school-portal.edu",
      "include_subdomains": false
    }
  ]
}
```

Поля:
- `domain` — доменное имя (обязательное)
- `include_subdomains` — включать поддомены (по умолчанию `true`)

## schedule.json — Расписание, время сна и параметры логирования

```json
{
  "entertainment_windows": [
    {
      "days": ["friday"],
      "start": "17:00",
      "end": "21:00",
      "limit_minutes": 120
    },
    {
      "days": ["saturday", "sunday"],
      "start": "10:00",
      "end": "20:00",
      "limit_minutes": 120
    }
  ],
  "sleep_times": [
    {
      "days": ["monday", "tuesday", "wednesday", "thursday", "friday"],
      "start": "22:00",
      "end": "07:00"
    },
    {
      "days": ["saturday", "sunday"],
      "start": "23:00",
      "end": "08:00"
    }
  ],
  "warning_before_minutes": 10,
  "sleep_warning_before_minutes": 15,
  "full_logging": false,
  "http_log_enabled": false,
  "http_log_port": 8080
}
```

Поля:
- `entertainment_windows` — временные окна для развлекательного контента
  - `days` — дни недели (monday...sunday)
  - `start`, `end` — время начала и окончания (HH:MM, 24-часовой)
  - `limit_minutes` — лимит развлекательного времени в минутах
- `sleep_times` — периоды сна (блокировка ВСЕХ программ, кроме системных)
  - `days` — дни недели
  - `start`, `end` — время начала и окончания (поддерживает переход через полночь)
- `warning_before_minutes` — за сколько минут до конца лимита развлечений показать предупреждение
- `sleep_warning_before_minutes` — за сколько минут до начала времени сна показать предупреждение
- `full_logging` — включить полное логирование всех программ и сайтов (true/false)
- `http_log_enabled` — включить HTTP-сервер для удалённого доступа к логам (true/false)
- `http_log_port` — порт HTTP-сервера логов (по умолчанию 8080, только LAN)

## GitHub Raw URL

Конфигурационные файлы загружаются по прямым ссылкам:
```
https://raw.githubusercontent.com/{owner}/{repo}/{branch}/allowed_apps.json
https://raw.githubusercontent.com/{owner}/{repo}/{branch}/allowed_sites.json
https://raw.githubusercontent.com/{owner}/{repo}/{branch}/schedule.json
```

URL задаются в локальном конфиге службы при установке.
