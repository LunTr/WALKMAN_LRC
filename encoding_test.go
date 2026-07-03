package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

func TestEnsureLRCFileUTF8ConvertsUTF16LE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf16.lrc")
	content := "[00:01.234]中文歌词"
	encoded := encodeUTF16LEWithBOM(content)

	if err := os.WriteFile(path, encoded, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := ensureLRCFileUTF8(path); err != nil {
		t.Fatalf("ensure utf-8: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read converted file: %v", err)
	}
	if !utf8.Valid(got) {
		t.Fatalf("converted file is not valid utf-8")
	}
	if string(got) != content {
		t.Fatalf("content changed: got %q, want %q", string(got), content)
	}
}

func encodeUTF16LEWithBOM(s string) []byte {
	runes := utf16.Encode([]rune(s))
	out := make([]byte, 2+len(runes)*2)
	out[0], out[1] = 0xFF, 0xFE
	for i, r := range runes {
		binary.LittleEndian.PutUint16(out[2+i*2:], r)
	}
	return out
}
