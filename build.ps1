$commit = (git rev-parse --short HEAD)
$ldflags = "-X parental-control-service/internal/version.GitCommit=$commit"

Write-Host "Building version: $commit"

go build -ldflags="$ldflags" -o service.exe ./cmd/service/
go build -ldflags="$ldflags -H windowsgui" -o tray.exe ./cmd/tray/
go build -ldflags="$ldflags -H windowsgui" -o browser-agent.exe ./cmd/browser-agent/
go build -ldflags="$ldflags" -o installer.exe ./cmd/installer/

Write-Host "Done. Built service.exe, tray.exe, browser-agent.exe, installer.exe ($commit)"
