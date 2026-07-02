@echo off
REM Build script: ensure the Go backend exists before Tauri builds
cd /d "%~dp0..\.."
if not exist lrc-proc-backend.exe (
    echo [tauri] Building Go backend...
    go build -buildvcs=false -o lrc-proc-backend.exe .
    if errorlevel 1 (
        echo [tauri] Go build failed!
        exit /b 1
    )
    echo [tauri] Go backend built successfully.
) else (
    echo [tauri] lrc-proc-backend.exe already exists.
)
echo [tauri] Copying binary to src-tauri/binaries...
if not exist tauri-app\src-tauri\binaries mkdir tauri-app\src-tauri\binaries
copy /Y lrc-proc-backend.exe tauri-app\src-tauri\binaries\ >nul
echo [tauri] Done.
