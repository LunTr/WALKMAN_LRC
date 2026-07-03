package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ─── LRC processing logic (port of lrcread.py) ───────────────────────

var timePattern = regexp.MustCompile(`\d{2}:\d{2}\.\d{2}`)
var fullMillisPattern = regexp.MustCompile(`\[\d{2}:\d{2}\.\d{3}`)

func parseTime(t string) (float64, bool) {
	parts := strings.Split(t, ":")
	if len(parts) != 2 {
		return 0, false
	}
	m, err1 := strconv.Atoi(parts[0])
	s, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return float64(m)*60 + s, true
}

func formatTime(total float64) string {
	m := int(total / 60)
	s := total - float64(m)*60
	return fmt.Sprintf("[%02d:%05.2f]", m, s)
}

func processLRC(content string) (string, error) {
	lines := strings.Split(content, "\n")
	var result []string
	var previousTime string

	for _, line := range lines {
		// Strip last digit from time tag: [MM:SS.CCx] → [MM:SS.CC]
		line = regexp.MustCompile(`(\[\d{2}:\d{2}\.\d{2})\d(\])`).
			ReplaceAllString(line, "${1}${2}")

		// Nudge consecutive duplicates by +0.01 s
		match := timePattern.FindString(line)
		if match != "" {
			if match == previousTime {
				total, ok := parseTime(match)
				if ok {
					line = strings.Replace(line, match, formatTime(total+0.01), 1)
				}
			}
			previousTime = match
		}
		result = append(result, line)
	}

	return strings.Join(result, "\n"), nil
}

func processFile(filepath string) (string, error) {
	if err := ensureLRCFileUTF8(filepath); err != nil {
		return "", err
	}

	data, err := os.ReadFile(filepath)
	if err != nil {
		return "", fmt.Errorf("无法读取文件: %v", err)
	}
	return processLRC(string(data))
}

// ─── Helpers ─────────────────────────────────────────────────────────

func workDir() string {
	d, _ := os.Getwd()
	return filepath.Base(d)
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// ─── HTTP types ──────────────────────────────────────────────────────

type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	State   string `json:"state"` // "unprocessed" | "processed" | "error"
	Error   string `json:"error,omitempty"`
	Content string `json:"content,omitempty"`
}

type APIResponse struct {
	OK      bool        `json:"ok"`
	Files   []FileEntry `json:"files,omitempty"`
	Error   string      `json:"error,omitempty"`
	Message string      `json:"message,omitempty"`
}

// ─── Handlers ────────────────────────────────────────────────────────

func handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"ok":false,"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(APIResponse{OK: false, Error: "无法读取目录"})
		return
	}

	var files []FileEntry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".lrc") {
			continue
		}
		files = append(files, FileEntry{
			Name:  e.Name(),
			Path:  e.Name(),
			Size:  fileSize(e.Name()),
			State: detectState(e.Name()),
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(APIResponse{OK: true, Files: files})
}

func handleProcess(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"ok":false,"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Filenames []string `json:"filenames"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(APIResponse{OK: false, Error: "请求格式错误"})
		return
	}

	var results []FileEntry
	for _, name := range req.Filenames {
		entry := FileEntry{Name: name, Path: name, Size: fileSize(name)}
		content, err := processFile(name)
		if err != nil {
			entry.State = "error"
			entry.Error = err.Error()
		} else {
			entry.State = "processed"
			entry.Content = content
		}
		results = append(results, entry)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(APIResponse{OK: true, Files: results})
}

// handleDownload streams a processed file back as a text/plain attachment.
// The frontend POSTs { filename, content } and follows the redirect / blob.
func handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"ok":false,"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, req.Filename))
	w.Write([]byte(req.Content))
}

// ─── State detection ─────────────────────────────────────────────────

// detectState scans the first 20 lines of an LRC file. If any timestamp
// still carries its full three-digit centisecond, the file is unprocessed.
// A file with only [MM:SS.CC] timestamps is considered processed.
func detectState(name string) string {
	f, err := os.Open(name)
	if err != nil {
		return "unknown"
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		if lineNum > 20 {
			break
		}
		// fullMillisPattern matches [MM:SS.CCC  (10 chars incl. opening bracket)
		// which means a 3-digit centisecond — the unprocessed format.
		if fullMillisPattern.FindString(scanner.Text()) != "" {
			return "unprocessed"
		}
	}

	if lineNum > 0 {
		return "processed"
	}
	return "unknown"
}

// ─── Main ────────────────────────────────────────────────────────────

func main() {
	fmt.Fprintf(os.Stderr, "[lrc-proc] CWD: %s\n", workDir())

	// Routes
	http.HandleFunc("/api/files", handleList)
	http.HandleFunc("/api/process", handleProcess)
	http.HandleFunc("/api/download", handleDownload)

	// API-only backend. The native Tauri app owns the user interface.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(APIResponse{
			OK:      true,
			Message: "LRC backend is running. Use lrc-proc.exe to open the native app.",
		})
	})

	addr := "127.0.0.1:7890"
	fmt.Fprintf(os.Stderr, "[lrc-proc] API listening on %s\n", addr)
	fmt.Fprintf(os.Stderr, "[lrc-proc] Ctrl+C 退出\n\n")

	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("服务器启动失败: %v", err)
	}
}
