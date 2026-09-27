package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

const (
	ledDefaultDevice = "/dev/ttyUSB0"

	ledTCGETS2 = 0x802c542a
	ledTCSETS2 = 0x402c542b

	ledBOTHER = 0x1000
	ledCBAUD  = 0x100f
	ledCS8    = 0x0030
	ledCREAD  = 0x0080
	ledCLOCAL = 0x0800
	ledICANON = 0x0002
	ledECHO   = 0x0008
	ledECHOE  = 0x0010
	ledISIG   = 0x0001
	ledIEXTEN = 0x8000
	ledOPOST  = 0x0001
	ledIXON   = 0x0400
	ledIXOFF  = 0x1000
	ledIXANY  = 0x0800
	ledVMIN   = 6
	ledVTIME  = 5
)

// termios2 matches Linux struct termios2 on x86_64.
type termios2 struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [19]uint8
	Ispeed uint32
	Ospeed uint32
}

type led struct{ file *os.File }

func openLED(path string) (*led, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := configureLEDPort(file.Fd()); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("configure %s: %w", path, err)
	}
	return &led{file: file}, nil
}

func configureLEDPort(fd uintptr) error {
	var term termios2
	if err := ioctl(fd, ledTCGETS2, unsafe.Pointer(&term)); err != nil {
		return err
	}
	term.Iflag &^= ledIXON | ledIXOFF | ledIXANY
	term.Oflag &^= ledOPOST
	term.Lflag &^= ledICANON | ledECHO | ledECHOE | ledISIG | ledIEXTEN
	term.Cflag &^= ledCBAUD
	term.Cflag |= ledBOTHER | ledCS8 | ledCREAD | ledCLOCAL
	term.Cc[ledVMIN] = 1
	term.Cc[ledVTIME] = 0
	term.Ispeed = 10000
	term.Ospeed = 10000
	return ioctl(fd, ledTCSETS2, unsafe.Pointer(&term))
}

func (l *led) close() error { return l.file.Close() }

// write sends the controller packet one byte at a time, with the delay used
// by the upstream application. Theme, intensity, and speed are UI values in
// the range 1..5. The controller uses the inverse scale for the latter two.
func (l *led) write(theme, intensity, speed byte) error {
	if theme < 1 || theme > 5 || intensity < 1 || intensity > 5 || speed < 1 || speed > 5 {
		return fmt.Errorf("LED values must be between 1 and 5")
	}
	if theme == 4 {
		intensity, speed = 5, 5
	} else {
		intensity, speed = 6-intensity, 6-speed
	}
	packet := [5]byte{0xfa, theme, intensity, speed}
	packet[4] = packet[0] + packet[1] + packet[2] + packet[3]
	for _, b := range packet {
		if _, err := l.file.Write([]byte{b}); err != nil {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}
