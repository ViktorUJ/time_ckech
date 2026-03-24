# План реализации: Сервис родительского контроля

## Обзор

Пошаговая реализация Windows-службы на Go для родительского контроля. Каждый шаг строится на предыдущих, начиная с моделей данных и ядра логики, заканчивая интеграцией компонентов и точкой входа службы. Язык реализации: Go. Библиотека PBT: `pgregory.net/rapid`.

## Задачи

- [x] 1. Инициализация проекта и модели данных
  - [x] 1.1 Инициализировать Go-модуль и структуру каталогов
    - Создать `go.mod` с модулем `parental-control-service`
    - Создать структуру каталогов: `cmd/service/`, `cmd/installer/`, `internal/config/`, `internal/monitor/`, `internal/browser/`, `internal/scheduler/`, `internal/enforcer/`, `internal/sleepmode/`, `internal/state/`, `internal/logger/`, `internal/httplog/`
    - Добавить зависимости: `golang.org/x/sys/windows`, `golang.org/x/sys/windows/svc`, `pgregory.net/rapid`
    - _Требования: 1.1_

  - [x] 1.2 Создать модели данных конфигурации и состояния
    - Создать файл `internal/config/models.go` с типами: `AllowedAppsConfig`, `AllowedApp`, `AllowedSitesConfig`, `AllowedSite`, `ScheduleConfig`, `TimeWindow`, `SleepTimeSlot`, `Config`
    - Создать файл `internal/state/models.go` с типом `ServiceState`
    - Создать файл `internal/logger/models.go` с типом `LogEntry` и константами типов событий
    - Создать файл `internal/httplog/models.go` с типами `StatusResponse`, `LogsResponse`
    - Создать файл `internal/monitor/models.go` с типами `ProcessInfo`, `ProcessClassification`, `RawProcess`
    - Создать файл `internal/browser/models.go` с типом `BrowserActivity`
    - Создать файл `internal/scheduler/models.go` с типами `Mode`, `ScheduleState`
    - _Требования: 6.1, 12.1, 5.4, 9.2, 14.5, 14.6, 4.2, 8.2_

  - [x] 1.3 Написать property-тест для round-trip сериализации расписания
    - **Property 1: Круговая сериализация расписания (Schedule round-trip)**
    - Генерировать произвольные валидные `ScheduleConfig` через `rapid`, сериализовать в JSON и десериализовать обратно, проверить эквивалентность
    - **Validates: Requirements 6.1, 12.1, 13.1, 14.1**

- [x] 2. Config Manager — загрузка и кэширование конфигурации
  - [x] 2.1 Реализовать интерфейс HTTPClient и Config Manager
    - Создать `internal/config/client.go` с интерфейсом `HTTPClient`
    - Создать `internal/config/manager.go` с `ConfigManager`: поля `githubURLs`, `httpClient`, `current`, `lastModified`, `mu`
    - Реализовать метод `Load(ctx)` — загрузка трёх файлов с GitHub по raw URL, парсинг JSON, кэширование с ETag/Last-Modified
    - Реализовать метод `Current()` — потокобезопасное чтение текущей конфигурации
    - При ошибке загрузки — сохранять последнюю успешную конфигурацию, логировать ошибку
    - При первом запуске без конфигурации — возвращать специальный флаг fail-closed
    - Реализовать сохранение кэша конфигурации на диск в `C:\ProgramData\ParentalControlService\config\`
    - _Требования: 2.1, 2.2, 2.3, 2.4, 2.5_

  - [x] 2.2 Написать property-тест для отката конфигурации при ошибке
    - **Property 12: Откат конфигурации при ошибке загрузки**
    - Генерировать последовательности успешных и неуспешных загрузок, проверить что после ошибки активная конфигурация = последняя успешная
    - **Validates: Requirements 2.4**

  - [x] 2.3 Написать property-тест для горячей перезагрузки конфигурации
    - **Property 13: Горячая перезагрузка конфигурации**
    - Генерировать пары валидных конфигураций A и B, загрузить B после A, проверить что текущая = B
    - **Validates: Requirements 2.3**

- [x] 3. Process Monitor — мониторинг и классификация процессов
  - [x] 3.1 Реализовать интерфейсы и классификатор процессов
    - Создать `internal/monitor/interfaces.go` с интерфейсами `ProcessEnumerator`, `ProcessClassifier`
    - Создать `internal/monitor/classifier.go` с реализацией классификации: системный (по пути `C:\Windows\`, `C:\Program Files\WindowsApps\`, по подписи Microsoft), разрешённый (по имени exe регистронезависимо, по пути с wildcard), неразрешённый
    - _Требования: 3.1, 3.2, 3.3, 4.2, 4.3_

  - [x] 3.2 Реализовать Process Monitor
    - Создать `internal/monitor/monitor.go` с `ProcessMonitor`
    - Реализовать метод `Scan(ctx)` — перечисление процессов через `ProcessEnumerator`, классификация каждого через `ProcessClassifier`
    - Реализовать Windows-реализацию `ProcessEnumerator` через Windows API (CreateToolhelp32Snapshot)
    - _Требования: 4.1, 4.2, 4.3_

  - [x] 3.3 Написать property-тест для классификации системных процессов
    - **Property 2: Классификация системных процессов**
    - Генерировать пути из `C:\Windows\...` и `C:\Program Files\WindowsApps\...`, проверить что всегда `ProcessSystem`
    - **Validates: Requirements 3.1, 3.2**

  - [x] 3.4 Написать property-тест для классификации по списку разрешённых
    - **Property 3: Классификация процессов по списку разрешённых**
    - Генерировать процессы и списки разрешённых, проверить корректность классификации (allowed/restricted)
    - **Validates: Requirements 4.2, 4.3**

- [x] 4. Browser Monitor — мониторинг активности в браузерах
  - [x] 4.1 Реализовать Browser Monitor и классификацию URL
    - Создать `internal/browser/interfaces.go` с интерфейсом `UIAutomation`
    - Создать `internal/browser/matcher.go` с функцией сопоставления URL по доменному имени (включая поддомены при `include_subdomains=true`)
    - Создать `internal/browser/monitor.go` с `BrowserMonitor`: метод `Scan(ctx)` для получения активных URL из Chrome, Edge, Firefox через UI Automation
    - Реализовать метод `RedirectTab(ctx, activity)` для перенаправления на страницу-заглушку
    - _Требования: 8.1, 8.2, 8.4, 8.5_

  - [x] 4.2 Написать property-тест для классификации URL по домену
    - **Property 4: Классификация URL по доменному имени с поддержкой поддоменов**
    - Генерировать URL и списки разрешённых сайтов, проверить корректность сопоставления (домен, поддомен, include_subdomains)
    - **Validates: Requirements 8.2, 8.4**

- [x] 5. Checkpoint — проверка базовых компонентов
  - Ensure all tests pass, ask the user if questions arise.

- [x] 6. Scheduler — расписание и определение режима
  - [x] 6.1 Реализовать Scheduler
    - Создать `internal/scheduler/interfaces.go` с интерфейсом `Clock`
    - Создать `internal/scheduler/scheduler.go` с `Scheduler`
    - Реализовать `CurrentState(now)` — определение текущего режима (`ModeOutsideWindow`, `ModeInsideWindow`, `ModeSleepTime`) с приоритетом времени сна
    - Реализовать `IsSleepTime(now)` — проверка попадания в период сна (с поддержкой перехода через полночь)
    - Реализовать `ActiveWindow(now)` — получение активного временного окна
    - Реализовать логику предупреждений: за `warning_before_minutes` до конца лимита и за `sleep_warning_before_minutes` до начала сна
    - _Требования: 6.1, 6.2, 6.3, 6.4, 6.5, 12.1, 12.4, 12.5_

  - [x] 6.2 Написать property-тест для подсчёта развлекательного времени
    - **Property 5: Подсчёт развлекательного времени по wall clock**
    - Генерировать последовательности тиков с разным количеством неразрешённых программ, проверить что счётчик увеличивается на elapsed time (не суммарно по программам)
    - **Validates: Requirements 5.1, 5.2, 8.3**

  - [x] 6.3 Написать property-тест для сброса счётчика
    - **Property 6: Сброс счётчика при новом временном окне**
    - Генерировать переходы между временными окнами, проверить сброс счётчика в ноль
    - **Validates: Requirements 5.3**

  - [x] 6.4 Написать property-тест для предупреждений перед порогом
    - **Property 8: Предупреждение перед порогом**
    - Генерировать состояния с разным оставшимся временем, проверить генерацию предупреждений при достижении порога
    - **Validates: Requirements 6.5, 12.5**

  - [x] 6.5 Написать property-тест для приоритета времени сна
    - **Property 9: Приоритет времени сна и полная блокировка**
    - Генерировать расписания с пересечением sleep time и entertainment window, проверить что sleep time всегда побеждает
    - **Validates: Requirements 12.2, 12.4**

- [x] 7. Enforcer — блокировка программ и уведомления
  - [x] 7.1 Реализовать Enforcer
    - Создать `internal/enforcer/interfaces.go` с интерфейсами `ProcessKiller`, `Notifier`
    - Создать `internal/enforcer/enforcer.go` с `Enforcer`
    - Реализовать `TerminateProcess(ctx, pid)` — graceful kill, ожидание 5 сек, force kill
    - Реализовать `BlockWithWarning(ctx, pid, message)` — показ уведомления + завершение процесса
    - Реализовать функцию принятия решения `ShouldBlock(mode, entertainmentSeconds, limitMinutes)` — разрешить/заблокировать на основе режима и лимита
    - _Требования: 7.1, 7.2, 7.3, 7.4_

  - [x] 7.2 Написать property-тест для решения о блокировке
    - **Property 7: Решение о блокировке неразрешённых программ**
    - Генерировать режимы, значения счётчика и лимиты, проверить что блокировка = NOT (inside_window AND under_limit)
    - **Validates: Requirements 6.2, 6.3, 6.4**

  - [x] 7.3 Написать property-тест для протокола завершения процессов
    - **Property 10: Протокол завершения процессов (graceful → force)**
    - Генерировать сценарии с успешным/неуспешным graceful kill, проверить что force kill вызывается только после таймаута
    - **Validates: Requirements 7.4**

- [x] 8. Sleep Mode Manager, State Manager, Logger
  - [x] 8.1 Реализовать Sleep Mode Manager
    - Создать `internal/sleepmode/manager.go` с `SleepModeManager`
    - Реализовать `Enforce(ctx, processes)` — завершение всех пользовательских процессов (включая разрешённые) с уведомлением
    - Реализовать `WarnUpcoming(minutesLeft)` — предупреждение о скором наступлении времени сна
    - _Требования: 12.2, 12.3, 12.5_

  - [x] 8.2 Реализовать State Manager
    - Создать `internal/state/manager.go` с `StateManager`
    - Реализовать `Save(state)` — атомарная запись JSON в файл в защищённом каталоге
    - Реализовать `Load()` — чтение состояния с диска, обработка повреждённого/отсутствующего файла
    - Реализовать логику восстановления: если текущее время в том же окне — восстановить счётчик, иначе — начать с нуля
    - _Требования: 5.4, 10.1, 10.2, 10.3_

  - [x] 8.3 Написать property-тест для восстановления состояния
    - **Property 11: Восстановление состояния после перезагрузки**
    - Генерировать сохранённые состояния и текущее время, проверить корректность восстановления счётчика (тот же окно → восстановить, другое → ноль)
    - **Validates: Requirements 10.1, 10.2**

  - [x] 8.4 Реализовать Logger
    - Создать `internal/logger/logger.go` с `Logger`
    - Создать `internal/logger/interfaces.go` с интерфейсом `EventLogWriter`
    - Создать `internal/logger/fulllog.go` с `FullLogWriter` — запись в файл JSON Lines с ротацией (50 МБ, 3 файла)
    - Реализовать `LogEvent(entry)` — запись в Event Log + Full Log (если включён)
    - Реализовать регистрацию источника событий "ParentalControlService"
    - _Требования: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 13.1, 13.2, 13.3, 13.4, 13.5_

  - [x] 8.5 Написать property-тест для полноты записей в логе
    - **Property 14: Полнота записей в логе**
    - Генерировать события разных типов, проверить наличие всех обязательных полей в записи
    - **Validates: Requirements 9.2, 9.3, 9.4**

  - [x] 8.6 Написать property-тест для полного логирования
    - **Property 15: Полное логирование всей активности**
    - Генерировать активности (разрешённые и неразрешённые) с full_logging=true/false, проверить что при true записываются все, при false — только неразрешённые
    - **Validates: Requirements 13.2, 13.3**

- [x] 9. Checkpoint — проверка ядра логики
  - Ensure all tests pass, ask the user if questions arise.

- [x] 10. HTTP Log Server — удалённый доступ к логам
  - [x] 10.1 Реализовать HTTP Log Server
    - Создать `internal/httplog/server.go` с `HTTPLogServer`
    - Реализовать `Start(ctx)` и `Stop(ctx)` с graceful shutdown (таймаут 10 сек)
    - Реализовать `lanOnlyMiddleware` — проверка IP-адреса (192.168.0.0/16, 10.0.0.0/8, 172.16.0.0/12, 127.0.0.0/8), отклонение внешних с 403
    - Реализовать `handleLogs` — GET /logs, возврат последних записей в JSON
    - Реализовать `handleStatus` — GET /status, возврат текущего состояния (`StatusResponse`)
    - _Требования: 14.1, 14.2, 14.3, 14.4, 14.5, 14.6, 14.7_

  - [x] 10.2 Написать property-тест для валидации LAN IP-адресов
    - **Property 16: Валидация IP-адресов для LAN-доступа**
    - Генерировать IP-адреса из LAN и не-LAN диапазонов, проверить корректность функции `isLANAddress`
    - **Validates: Requirements 14.3, 14.4**

- [x] 11. Интеграция компонентов и основной цикл сервиса
  - [x] 11.1 Реализовать основной цикл сервиса
    - Создать `internal/service/service.go` с `Service` — оркестратор всех компонентов
    - Реализовать основной цикл (тик каждые 5 сек): проверка расписания → сканирование процессов → классификация → мониторинг браузеров → обновление счётчика → применение правил → сохранение состояния (каждые 30 сек)
    - Реализовать фоновую горутину обновления конфигурации (каждые 5 минут)
    - Реализовать обработку изменений `full_logging` и `http_log_enabled` при обновлении конфигурации
    - Реализовать graceful shutdown: сохранение состояния, остановка HTTP-сервера, запись события в лог
    - _Требования: 4.1, 5.1, 5.2, 5.3, 5.4, 6.2, 6.3, 6.4, 7.1, 7.2, 7.3, 8.3, 12.2, 12.3, 13.5, 14.7_

  - [x] 11.2 Реализовать точку входа Windows-службы
    - Создать `cmd/service/main.go` — регистрация и запуск через `svc.Run`
    - Реализовать интерфейс `svc.Handler` (Execute) с обработкой команд Start/Stop/Interrogate
    - Настроить Recovery: автоматический перезапуск при аварийном завершении
    - _Требования: 1.1, 1.2, 1.3, 11.1, 11.3_

  - [x] 11.3 Реализовать утилиту установки службы
    - Создать `cmd/installer/main.go` — установка/удаление Windows-службы
    - Настроить тип запуска "Автоматически", учётную запись Local System, параметры Recovery
    - Регистрация источника событий "ParentalControlService" в Event Log
    - Создание защищённого каталога `C:\ProgramData\ParentalControlService\` с правами SYSTEM only
    - Создание файла страницы-заглушки `blocked.html`
    - _Требования: 1.1, 9.1, 10.3, 11.1, 11.2_

- [x] 12. Финальный checkpoint
  - Ensure all tests pass, ask the user if questions arise.

## Примечания

- Задачи с `*` — опциональные (тесты), могут быть пропущены для ускорения MVP
- Каждая задача ссылается на конкретные требования для трассируемости
- Checkpoints обеспечивают инкрементальную валидацию
- Property-тесты проверяют универсальные свойства корректности
- Unit-тесты проверяют конкретные примеры и граничные случаи
- Все внешние зависимости абстрагированы через интерфейсы для тестируемости
