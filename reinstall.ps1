$configUrl = "https://drive.google.com/uc?export=download&id=1fQ4Rkg_myHQnzOa9hfvopoTm8BgybOBg"
Start-Process -Verb RunAs -FilePath ".\installer.exe" -ArgumentList @(
    "--silent",
    "--config-url", $configUrl,
    "--password", "5",
    "install"
) -Wait
