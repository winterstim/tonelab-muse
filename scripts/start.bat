@echo off
setlocal enabledelayedexpansion

cd /d "%~dp0"

if "%MODEL_API_KEY%"=="" (
    if exist "%~dp0.model_api_key" (
        set /p MODEL_API_KEY=<"%~dp0.model_api_key"
    )
)

if "%MODEL_API_KEY%"=="" (
    set /p KEY="Enter MODEL_API_KEY: "
    set "MODEL_API_KEY=!KEY!"
    echo !KEY!>"%~dp0.model_api_key"
)

if exist "%~dp0tonelab-muse-windows-amd64.exe" (
    start "" "%~dp0tonelab-muse-windows-amd64.exe" serve
) else if exist "%~dp0tonelab-muse.exe" (
    start "" "%~dp0tonelab-muse.exe" serve
) else (
    echo Error: tonelab-muse executable not found.
    pause
    exit /b 1
)

start "" http://localhost:7400
