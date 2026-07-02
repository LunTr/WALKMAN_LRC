// Prevents additional console window on Windows in release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use reqwest::blocking::Client;
use serde::{Deserialize, Serialize};
use std::path::PathBuf;
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};

#[cfg(windows)]
use std::os::windows::process::CommandExt;

#[cfg(windows)]
const GO_BINARY_NAME: &str = "lrc-proc-backend.exe";
#[cfg(not(windows))]
const GO_BINARY_NAME: &str = "lrc-proc-backend";
#[cfg(windows)]
const CREATE_NO_WINDOW: u32 = 0x08000000;

// ─── Types ─────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
struct FileEntry {
    name: String,
    path: String,
    size: i64,
    state: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    content: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct ApiResponse {
    ok: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    files: Option<Vec<FileEntry>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    message: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct ProcessRequest {
    filenames: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct DownloadRequest {
    filename: String,
    content: String,
}

// ─── Go process manager ────────────────────────────────────────────

#[derive(Clone)]
struct GoProcess {
    child: Arc<Mutex<Option<Child>>>,
    port: Arc<Mutex<u16>>,
    scan_dir: Arc<Mutex<Option<PathBuf>>>,
}

impl GoProcess {
    fn new() -> Self {
        Self {
            child: Arc::new(Mutex::new(None)),
            port: Arc::new(Mutex::new(7890)),
            scan_dir: Arc::new(Mutex::new(None)),
        }
    }

    /// Find the Go backend binary.
    ///
    /// The public app entrypoint is also named lrc-proc.exe, so the
    /// HTTP backend must use a separate filename to avoid recursively
    /// launching the Tauri app when it starts the backend.
    fn find_go_binary() -> Option<std::path::PathBuf> {
        let mut roots: Vec<std::path::PathBuf> = vec![];

        if let Ok(exe) = std::env::current_exe() {
            if let Some(dir) = exe.parent() {
                roots.push(dir.to_path_buf());
            }
        }
        if let Ok(cwd) = std::env::current_dir() {
            roots.push(cwd);
        }

        for root in roots {
            let mut dir = root.as_path();
            // Walk up to 4 parent levels to cover cargo's target/debug/,
            // src-tauri/, and the project root itself.
            for _ in 0..5 {
                let candidate = dir.join(GO_BINARY_NAME);
                if candidate.exists() {
                    return Some(candidate);
                }
                match dir.parent() {
                    Some(parent) => dir = parent,
                    None => break,
                }
            }
        }

        None
    }

    /// Start the Go subprocess and discover its port via scanning.
    fn start(&self) -> Result<u16, String> {
        let binary = Self::find_go_binary().ok_or_else(|| {
            format!(
                "未找到 {}，请先在项目根目录运行 `go build -o {} .` 构建 Go 后端",
                GO_BINARY_NAME, GO_BINARY_NAME
            )
        })?;
        let fallback_dir = binary
            .parent()
            .map(|p| p.to_path_buf())
            .unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")));
        let scan_dir = self
            .scan_dir
            .lock()
            .unwrap()
            .clone()
            .unwrap_or(fallback_dir);

        if !scan_dir.is_dir() {
            return Err(format!("扫描路径不是有效目录: {}", scan_dir.display()));
        }

        // Kill any previous instance
        self.kill();

        // Start the Go process
        let child = Command::new(&binary)
            .current_dir(&scan_dir)
            .stdout(Stdio::inherit())
            .stderr(Stdio::inherit())
            .spawn()
            .map_err(|e| format!("无法启动 Go 后端: {}", e))?;

        *self.child.lock().unwrap() = Some(child);
        *self.scan_dir.lock().unwrap() = Some(scan_dir);

        // Wait for server to become ready, then scan for port
        std::thread::sleep(std::time::Duration::from_millis(800));

        let discovered_port = self.discover_port()?;
        *self.port.lock().unwrap() = discovered_port;
        Ok(discovered_port)
    }

    /// Scan 7890-7999 for the Go server's listening port.
    fn discover_port(&self) -> Result<u16, String> {
        let client = Client::builder()
            .timeout(std::time::Duration::from_millis(300))
            .build()
            .map_err(|e| e.to_string())?;

        for port in 7890..8000 {
            let url = format!("http://127.0.0.1:{}/api/files", port);
            if client.get(&url).send().map(|r| r.status().is_success()).unwrap_or(false) {
                return Ok(port);
            }
        }

        // Fallback: try default
        Ok(7890)
    }

    fn port(&self) -> u16 {
        *self.port.lock().unwrap()
    }

    fn base_url(&self) -> String {
        format!("http://127.0.0.1:{}", self.port())
    }

    fn scan_dir(&self) -> PathBuf {
        if let Some(dir) = self.scan_dir.lock().unwrap().clone() {
            return dir;
        }

        Self::find_go_binary()
            .and_then(|p| p.parent().map(|parent| parent.to_path_buf()))
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."))
    }

    fn set_scan_dir(&self, dir: PathBuf) -> Result<u16, String> {
        let dir = dir
            .canonicalize()
            .map_err(|e| format!("无法访问扫描路径: {}", e))?;

        if !dir.is_dir() {
            return Err(format!("扫描路径不是有效目录: {}", dir.display()));
        }

        *self.scan_dir.lock().unwrap() = Some(dir);
        self.start()
    }

    fn kill(&self) {
        if let Some(mut child) = self.child.lock().unwrap().take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }
}

// ─── Global state ──────────────────────────────────────────────────

struct AppState {
    go: GoProcess,
}

// ─── Tauri Commands ────────────────────────────────────────────────

#[tauri::command]
fn get_app_info(state: tauri::State<AppState>) -> Result<String, String> {
    Ok(state.go.base_url())
}

#[tauri::command]
fn get_scan_dir(state: tauri::State<AppState>) -> Result<String, String> {
    Ok(state.go.scan_dir().display().to_string())
}

#[tauri::command]
fn set_scan_dir(state: tauri::State<AppState>, path: String) -> Result<String, String> {
    let dir = PathBuf::from(path);
    state.go.set_scan_dir(dir)?;
    Ok(state.go.scan_dir().display().to_string())
}

#[tauri::command]
fn choose_scan_dir(state: tauri::State<AppState>) -> Result<Option<String>, String> {
    let Some(dir) = pick_directory(state.go.scan_dir())? else {
        return Ok(None);
    };

    state.go.set_scan_dir(dir)?;
    Ok(Some(state.go.scan_dir().display().to_string()))
}

#[tauri::command]
fn list_files(state: tauri::State<AppState>) -> Result<Vec<FileEntry>, String> {
    let url = format!("{}/api/files", state.go.base_url());
    let resp: ApiResponse = reqwest::blocking::get(&url)
        .map_err(|e| format!("请求失败: {}", e))?
        .json()
        .map_err(|e| format!("解析失败: {}", e))?;

    resp.files.ok_or_else(|| resp.error.unwrap_or_else(|| "未知错误".to_string()))
}

#[tauri::command]
fn process_files(
    state: tauri::State<AppState>,
    filenames: Vec<String>,
) -> Result<Vec<FileEntry>, String> {
    let url = format!("{}/api/process", state.go.base_url());
    let req = ProcessRequest { filenames };

    let resp: ApiResponse = reqwest::blocking::Client::new()
        .post(&url)
        .json(&req)
        .send()
        .map_err(|e| format!("请求失败: {}", e))?
        .json()
        .map_err(|e| format!("解析失败: {}", e))?;

    resp.files.ok_or_else(|| resp.error.unwrap_or_else(|| "未知错误".to_string()))
}

#[tauri::command]
fn download_file(
    state: tauri::State<AppState>,
    filename: String,
    content: String,
) -> Result<String, String> {
    let url = format!("{}/api/download", state.go.base_url());
    let req = DownloadRequest { filename, content };

    let resp = reqwest::blocking::Client::new()
        .post(&url)
        .json(&req)
        .send()
        .map_err(|e| format!("下载失败: {}", e))?;

    if !resp.status().is_success() {
        return Err(format!("HTTP {}", resp.status()));
    }

    Ok(resp.text().map_err(|e| e.to_string())?)
}

#[cfg(windows)]
fn pick_directory(initial_dir: PathBuf) -> Result<Option<PathBuf>, String> {
    let script = r#"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择 LRC 扫描目录'
$dialog.ShowNewFolderButton = $false
$initial = [Environment]::GetEnvironmentVariable('LRC_PROC_INITIAL_DIR', 'Process')
if ($initial -and (Test-Path -LiteralPath $initial)) {
  $dialog.SelectedPath = $initial
}
$result = $dialog.ShowDialog()
if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
  Write-Output $dialog.SelectedPath
  exit 0
}
exit 2
"#;

    let output = Command::new("powershell.exe")
        .args(["-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-Command", script])
        .env("LRC_PROC_INITIAL_DIR", initial_dir)
        .creation_flags(CREATE_NO_WINDOW)
        .output()
        .map_err(|e| format!("无法打开目录选择窗口: {}", e))?;

    if output.status.code() == Some(2) {
        return Ok(None);
    }
    if !output.status.success() {
        let err = String::from_utf8_lossy(&output.stderr).trim().to_string();
        return Err(if err.is_empty() {
            "目录选择窗口已异常退出".to_string()
        } else {
            err
        });
    }

    let selected = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if selected.is_empty() {
        Ok(None)
    } else {
        Ok(Some(PathBuf::from(selected)))
    }
}

#[cfg(not(windows))]
fn pick_directory(_initial_dir: PathBuf) -> Result<Option<PathBuf>, String> {
    Err("当前目录选择功能暂只支持 Windows".to_string())
}

// ─── Main ──────────────────────────────────────────────────────────

fn main() {
    let go_process = GoProcess::new();

    // Start before the webview loads so the first file scan does not race
    // the backend process startup.
    match go_process.start() {
        Ok(port) => {
            eprintln!("[tauri] Go backend ready on port {}", port);
        }
        Err(e) => {
            eprintln!("[tauri] Warning: Go backend not available: {}", e);
        }
    }

    tauri::Builder::default()
        .manage(AppState { go: go_process })
        .invoke_handler(tauri::generate_handler![
            get_app_info,
            get_scan_dir,
            set_scan_dir,
            choose_scan_dir,
            list_files,
            process_files,
            download_file,
        ])
        .run(tauri::generate_context!())
        .expect("Tauri 应用启动失败");
}
