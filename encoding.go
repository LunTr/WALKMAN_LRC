package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

const (
	windowsCPACP       = 0
	windowsErrInvalid  = 8
	replacementRuneStr = "\uFFFD"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetACP          = kernel32.NewProc("GetACP")
	procMultiByteToWide = kernel32.NewProc("MultiByteToWideChar")
)

// ensureLRCFileUTF8 converts a non-UTF-8 LRC file to UTF-8 in place.
func ensureLRCFileUTF8(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("无法读取文件: %v", err)
	}

	text, encodingName, err := decodeText(data)
	if err != nil {
		return err
	}
	if encodingName == "utf-8" {
		return nil
	}

	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		return fmt.Errorf("无法转换为 UTF-8: %v", err)
	}
	return nil
}

func decodeText(data []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:]), "utf-8", nil
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		text, err := decodeUTF16(data[2:], binary.LittleEndian)
		return text, "utf-16le", err
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		text, err := decodeUTF16(data[2:], binary.BigEndian)
		return text, "utf-16be", err
	case utf8.Valid(data):
		return string(data), "utf-8", nil
	default:
		text, err := decodeSystemANSI(data)
		if err != nil {
			return "", "", err
		}
		if strings.Contains(text, replacementRuneStr) {
			return "", "", fmt.Errorf("无法识别文件编码")
		}
		return text, "ansi", nil
	}
}

func decodeUTF16(data []byte, order binary.ByteOrder) (string, error) {
	if len(data)%2 != 0 {
		return "", fmt.Errorf("UTF-16 文件长度无效")
	}

	u16 := make([]uint16, len(data)/2)
	for i := range u16 {
		u16[i] = order.Uint16(data[i*2:])
	}
	return string(utf16.Decode(u16)), nil
}

func decodeSystemANSI(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("当前平台暂不支持自动转换非 UTF-8 编码")
	}

	codePage := windowsACP()
	wideLen, _, err := procMultiByteToWide.Call(
		uintptr(codePage),
		uintptr(windowsErrInvalid),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		0,
		0,
	)
	if wideLen == 0 {
		return "", fmt.Errorf("无法按系统 ANSI 编码解码文件: %v", err)
	}

	wide := make([]uint16, wideLen)
	written, _, err := procMultiByteToWide.Call(
		uintptr(codePage),
		uintptr(windowsErrInvalid),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&wide[0])),
		uintptr(len(wide)),
	)
	if written == 0 {
		return "", fmt.Errorf("无法按系统 ANSI 编码解码文件: %v", err)
	}

	return string(utf16.Decode(wide[:written])), nil
}

func windowsACP() uint32 {
	cp, _, _ := procGetACP.Call()
	if cp == 0 {
		return windowsCPACP
	}
	return uint32(cp)
}
