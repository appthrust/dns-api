package main

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyControllerArchitecture(t *testing.T) {
	amd64 := writeELFHeader(t, elf.EM_X86_64)
	arm64 := writeELFHeader(t, elf.EM_AARCH64)
	for _, tc := range []struct {
		name, path, target string
		wantErr            bool
	}{
		{name: "amd64 executable", path: amd64, target: "linux/amd64"},
		{name: "arm64 executable", path: arm64, target: "linux/arm64"},
		{name: "amd64 mislabeled as arm64", path: amd64, target: "linux/arm64", wantErr: true},
		{name: "arm64 mislabeled as amd64", path: arm64, target: "linux/amd64", wantErr: true},
		{name: "unsupported target", path: amd64, target: "darwin/amd64", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verify(tc.path, tc.target)
			if (err != nil) != tc.wantErr {
				t.Fatalf("verify(%s, %s) = %v, want error %v", tc.path, tc.target, err, tc.wantErr)
			}
		})
	}
}

func TestVerifyRejectsNonELFController(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager")
	if err := os.WriteFile(path, []byte("not an executable"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(path, "linux/amd64"); err == nil {
		t.Fatal("non-ELF controller was accepted")
	}
}

func writeELFHeader(t *testing.T, machine elf.Machine) string {
	t.Helper()
	var header [64]byte
	copy(header[:], elf.ELFMAG)
	header[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	header[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	header[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	binary.LittleEndian.PutUint16(header[16:18], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:20], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(header[52:54], uint16(len(header)))
	path := filepath.Join(t.TempDir(), "manager")
	if err := os.WriteFile(path, header[:], 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
