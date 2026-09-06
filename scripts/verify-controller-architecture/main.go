// Command verify-controller-architecture checks the executable, not the OCI
// platform label, before the controller can enter a published runtime image.
package main

import (
	"debug/elf"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: verify-controller-architecture <binary> <target-platform>")
		os.Exit(2)
	}
	if err := verify(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("verified controller executable for %s\n", os.Args[2])
}

func verify(path, target string) (err error) {
	var machine elf.Machine
	switch target {
	case "linux/amd64":
		machine = elf.EM_X86_64
	case "linux/arm64":
		machine = elf.EM_AARCH64
	default:
		return fmt.Errorf("unsupported controller target platform %q", target)
	}

	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("read controller ELF: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close controller ELF: %w", closeErr)
		}
	}()

	// TARGETPLATFORM is independent of the GOARCH build argument: checking
	// against GOARCH would accept the original accidental AMD64 override.
	if file.Class != elf.ELFCLASS64 || file.Data != elf.ELFDATA2LSB || file.Machine != machine {
		return fmt.Errorf("controller architecture mismatch for %s: got %s/%s/%s, want ELFCLASS64/ELFDATA2LSB/%s",
			target, file.Class, file.Data, file.Machine, machine)
	}
	return nil
}
