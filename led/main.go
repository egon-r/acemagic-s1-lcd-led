// LED serial test utility. This is experimental; see README.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func ioctl(fd uintptr, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

func main() {
	device := flag.String("device", ledDefaultDevice, "LED serial device")
	theme := flag.Int("theme", 1, "LED theme (1-5)")
	intensity := flag.Int("intensity", 5, "LED intensity (1-5)")
	speed := flag.Int("speed", 5, "LED speed (1-5)")
	flag.Parse()
	if *theme < 1 || *theme > 5 || *intensity < 1 || *intensity > 5 || *speed < 1 || *speed > 5 {
		fmt.Fprintln(os.Stderr, "theme, intensity and speed must be 1-5")
		os.Exit(2)
	}
	port, err := openLED(*device)
	if err == nil {
		err = port.write(byte(*theme), byte(*intensity), byte(*speed))
		if closeErr := port.close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
