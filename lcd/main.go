// s1display drives the Acemagic S1 front LCD through Linux usbfs ioctls.
// It deliberately has no external Go dependencies.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	width  = 320
	height = 170

	// Upstream builds 4105 bytes for hidapi, including a dummy report ID.
	// The LCD descriptor has no report IDs and declares 4104 output bytes.
	// hidapi strips the dummy zero; direct usbfs must do the same.
	reportLen  = 1 + 8 + 4096
	headerOff  = 1
	dataOff    = 1 + 8
	dataLen    = 4096
	chunkCount = 27
	finalChunk = 2304

	usbdevfsBulk           = 0xc0185502
	usbdevfsClaimInterface = 0x8004550f
	usbdevfsReleaseIntf    = 0x80045510
	usbdevfsDisconnect     = 0x5516
	usbdevfsConnect        = 0x5517
	usbInterface           = 1
	usbEndpointOut         = 0x02
)

// Matches struct usbdevfs_bulktransfer in linux/usbdevice_fs.h on amd64.
type usbdevfsBulkTransfer struct {
	Endpoint uint32
	Length   uint32
	Timeout  uint32
	Data     unsafe.Pointer
}

type lcd struct {
	file *os.File
	mu   sync.Mutex // serializes all device writes
}

func openLCD(path string) (*lcd, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	interfaceNumber := int32(usbInterface)
	_ = ioctl(file.Fd(), usbdevfsDisconnect, unsafe.Pointer(&interfaceNumber))
	if err := ioctl(file.Fd(), usbdevfsClaimInterface, unsafe.Pointer(&interfaceNumber)); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("claim USB interface %d: %w", usbInterface, err)
	}
	return &lcd{file: file}, nil
}

func (l *lcd) close() {
	interfaceNumber := int32(usbInterface)
	_ = ioctl(l.file.Fd(), usbdevfsReleaseIntf, unsafe.Pointer(&interfaceNumber))
	_ = ioctl(l.file.Fd(), usbdevfsConnect, unsafe.Pointer(&interfaceNumber))
	_ = l.file.Close()
}

func ioctl(fd uintptr, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// wireReport mirrors hidapi's handling of this device's unnumbered reports.
func wireReport(packet []byte) ([]byte, error) {
	if len(packet) != reportLen || packet[0] != 0 {
		return nil, fmt.Errorf("expected %d bytes with dummy report ID zero", reportLen)
	}
	return packet[1:], nil
}

// write sends one full report. All device writes must go through this method
// so that concurrent callers never interleave USB transfers.
func (l *lcd) write(packet []byte) error {
	packet, err := wireReport(packet)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	transfer := usbdevfsBulkTransfer{
		Endpoint: usbEndpointOut,
		Length:   uint32(len(packet)),
		Timeout:  5000,
		Data:     unsafe.Pointer(&packet[0]),
	}
	n, _, errno := syscall.Syscall(syscall.SYS_IOCTL, l.file.Fd(), usbdevfsBulk, uintptr(unsafe.Pointer(&transfer)))
	if errno != 0 {
		return errno
	}
	if int(n) != len(packet) {
		return fmt.Errorf("short LCD write: %d of %d bytes", n, len(packet))
	}
	return nil
}

// newReport returns a report buffer with the 8-byte header initialised to
// signature and command bytes. Callers fill the remaining header fields.
func newReport(command byte) []byte {
	packet := make([]byte, reportLen)
	packet[headerOff] = 0x55
	packet[headerOff+1] = command
	return packet
}

func (l *lcd) orientation(portrait bool) error {
	mode := byte(0x01) // LCD_LANDSCAPE
	if portrait {
		mode = 0x02 // LCD_PORTRAIT
	}
	packet := newReport(0xa1) // LCD_CONFIG
	packet[headerOff+2] = 0xf1
	packet[headerOff+3] = mode
	return l.write(packet)
}

func (l *lcd) heartbeat() error {
	now := time.Now()
	packet := newReport(0xa1)  // LCD_CONFIG
	packet[headerOff+2] = 0xf2 // LCD_SET_TIME
	packet[headerOff+3] = byte(now.Hour())
	packet[headerOff+4] = byte(now.Minute())
	packet[headerOff+5] = byte(now.Second())
	return l.write(packet)
}

// redraw sends a full frame the way upstream does: 27 chunks of 4096 bytes,
// except the last chunk which is 2304 bytes. The first chunk is marked with
// 0xf0 and the final one with 0xf2; the firmware only commits the frame on
// the 0xf2 chunk.
func (l *lcd) redraw(pixels []byte) error {
	if len(pixels) != width*height*2 {
		return fmt.Errorf("expected %d framebuffer bytes, got %d", width*height*2, len(pixels))
	}
	for index := 0; index < chunkCount; index++ {
		length := dataLen
		if index == chunkCount-1 {
			length = finalChunk
		}
		packet := newReport(0xa3) // LCD_REDRAW
		switch index {
		case 0:
			packet[headerOff+2] = 0xf0 // START
		case chunkCount - 1:
			packet[headerOff+2] = 0xf2 // END
		default:
			packet[headerOff+2] = 0xf1 // CONTINUE
		}
		packet[headerOff+3] = byte(1 + index) // sequence
		// Header fields are unaligned, matching upstream: offset at bytes 5-6,
		// length at bytes 7-8 (after the leading report byte).
		binary.BigEndian.PutUint16(packet[headerOff+5:], uint16(index*dataLen))
		binary.BigEndian.PutUint16(packet[headerOff+7:], uint16(length))
		copy(packet[dataOff:], pixels[index*dataLen:index*dataLen+length])
		if err := l.write(packet); err != nil {
			return fmt.Errorf("chunk %d: %w", index+1, err)
		}
	}
	return nil
}

// refreshFrame uses upstream's active paint command. Each tile must fit in
// 4096 data bytes, with width and height each fitting in one byte.
func refreshFrame(pixels []byte, send func([]byte) error) error {
	if len(pixels) != width*height*2 {
		return fmt.Errorf("expected %d framebuffer bytes, got %d", width*height*2, len(pixels))
	}
	const tileWidth = 160
	const tileHeight = dataLen / (tileWidth * 2)
	for x := 0; x < width; x += tileWidth {
		for y := 0; y < height; y += tileHeight {
			h := min(tileHeight, height-y)
			packet := newReport(0xa2)
			binary.LittleEndian.PutUint16(packet[headerOff+2:], uint16(x))
			binary.LittleEndian.PutUint16(packet[headerOff+4:], uint16(y))
			packet[headerOff+6], packet[headerOff+7] = tileWidth, byte(h)
			for row := 0; row < h; row++ {
				src := ((y+row)*width + x) * 2
				dst := dataOff + row*tileWidth*2
				copy(packet[dst:dst+tileWidth*2], pixels[src:src+tileWidth*2])
			}
			if err := send(packet); err != nil {
				return fmt.Errorf("refresh tile (%d,%d): %w", x, y, err)
			}
		}
	}
	return nil
}

func rgb565(img image.Image) []byte {
	out := make([]byte, width*height*2)
	for y := range height {
		for x := range width {
			r, g, b, _ := img.At(x, y).RGBA()
			binary.BigEndian.PutUint16(out[(y*width+x)*2:], uint16((r>>11)<<11|(g>>10)<<5|(b>>11)))
		}
	}
	return out
}

func loadImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			sx := img.Bounds().Min.X + x*img.Bounds().Dx()/width
			sy := img.Bounds().Min.Y + y*img.Bounds().Dy()/height
			out.Set(x, y, img.At(sx, sy))
		}
	}
	return out, nil
}

func textImage(lines []string) image.Image {
	return textImageSize(lines, width, height)
}

func textImageSize(lines []string, w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for row, line := range lines {
		line = strings.ToUpper(line)
		scale := min(6, max(1, (w-24)/(max(1, len(line))*6)))
		lineHeight := 8 * scale
		y := 12 + row*(lineHeight+10)
		if y+7*scale > h {
			break
		}
		drawText(img, (w-len(line)*6*scale)/2, y, line, scale)
	}
	return img
}

// statusTextImage reserves the upper portrait area for a large heading and
// draws later rows in a fixed small-font list. It avoids textImageSize's
// per-row scaling, which makes a long status list overlap after rotation.
func statusTextImage(lines []string, degrees int) image.Image {
	virtual := image.NewRGBA(image.Rect(0, 0, height, width))
	for row, line := range lines {
		line = strings.ToUpper(line)
		scale, y := 2, 58+(row-2)*24
		if row < 2 {
			scale, y = 3, 8+row*26
		}
		if y+7*scale > virtual.Bounds().Dy() {
			break
		}
		drawText(virtual, max(0, (height-len(line)*6*scale)/2), y, line, scale)
	}
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range width {
		for x := range height {
			if degrees == 90 {
				out.Set(width-1-y, x, virtual.At(x, y))
			} else {
				out.Set(y, height-1-x, virtual.At(x, y))
			}
		}
	}
	return out
}

func rotatedTextImage(lines []string, degrees int) image.Image {
	virtual := textImageSize(lines, height, width)
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range width {
		for x := range height {
			if degrees == 90 {
				out.Set(width-1-y, x, virtual.At(x, y))
			} else {
				out.Set(y, height-1-x, virtual.At(x, y))
			}
		}
	}
	return out
}

func drawText(img *image.RGBA, x, y int, text string, scale int) {
	for _, r := range text {
		glyph, ok := font5x7[r]
		if !ok {
			glyph = font5x7['?']
		}
		for gy, bits := range glyph {
			for gx := range 5 {
				if bits&(1<<(4-gx)) != 0 {
					for dy := range scale {
						for dx := range scale {
							img.SetRGBA(x+gx*scale+dx, y+gy*scale+dy, color.RGBA{255, 255, 255, 255})
						}
					}
				}
			}
		}
		x += 6 * scale
	}
}

func rotateImage(src image.Image, degrees int) image.Image {
	if degrees == 0 {
		return src
	}
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if degrees == 180 {
		out := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range h {
			for x := range w {
				out.Set(w-1-x, h-1-y, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
		return out
	}
	out := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := range h {
		for x := range w {
			if degrees == 90 {
				out.Set(h-1-y, x, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			} else if degrees == 270 {
				out.Set(y, w-1-x, src.At(bounds.Min.X+x, bounds.Min.Y+y))
			}
		}
	}
	return out
}

var font5x7 = map[rune][7]byte{
	' ': {0, 0, 0, 0, 0, 0, 0}, '?': {14, 17, 2, 4, 4, 0, 4},
	'0': {14, 17, 19, 21, 25, 17, 14}, '1': {4, 12, 4, 4, 4, 4, 14}, '2': {14, 17, 1, 2, 4, 8, 31}, '3': {30, 1, 1, 14, 1, 1, 30}, '4': {2, 6, 10, 18, 31, 2, 2}, '5': {31, 16, 30, 1, 1, 17, 14}, '6': {6, 8, 16, 30, 17, 17, 14}, '7': {31, 1, 2, 4, 8, 8, 8}, '8': {14, 17, 17, 14, 17, 17, 14}, '9': {14, 17, 17, 15, 1, 2, 12},
	'A': {14, 17, 17, 31, 17, 17, 17}, 'B': {30, 17, 17, 30, 17, 17, 30}, 'C': {14, 17, 16, 16, 16, 17, 14}, 'D': {30, 17, 17, 17, 17, 17, 30}, 'E': {31, 16, 16, 30, 16, 16, 31}, 'F': {31, 16, 16, 30, 16, 16, 16}, 'G': {14, 17, 16, 23, 17, 17, 14}, 'H': {17, 17, 17, 31, 17, 17, 17}, 'I': {14, 4, 4, 4, 4, 4, 14}, 'J': {1, 1, 1, 1, 17, 17, 14}, 'K': {17, 18, 20, 24, 20, 18, 17}, 'L': {16, 16, 16, 16, 16, 16, 31}, 'M': {17, 27, 21, 21, 17, 17, 17}, 'N': {17, 25, 21, 19, 17, 17, 17}, 'O': {14, 17, 17, 17, 17, 17, 14}, 'P': {30, 17, 17, 30, 16, 16, 16}, 'Q': {14, 17, 17, 17, 21, 18, 13}, 'R': {30, 17, 17, 30, 20, 18, 17}, 'S': {15, 16, 16, 14, 1, 1, 30}, 'T': {31, 4, 4, 4, 4, 4, 4}, 'U': {17, 17, 17, 17, 17, 17, 14}, 'V': {17, 17, 17, 17, 17, 10, 4}, 'W': {17, 17, 17, 21, 21, 21, 10}, 'X': {17, 17, 10, 4, 10, 17, 17}, 'Y': {17, 17, 10, 4, 4, 4, 4}, 'Z': {31, 1, 2, 4, 8, 16, 31},
	'.': {0, 0, 0, 0, 0, 6, 6}, ':': {0, 6, 6, 0, 6, 6, 0}, '-': {0, 0, 0, 31, 0, 0, 0}, '/': {1, 2, 4, 8, 16, 0, 0},
}

const defaultSocket = "/run/s1display.sock"

type command struct {
	Image    string   `json:"image,omitempty"`
	Text     []string `json:"text,omitempty"`
	Rotate   int      `json:"rotate,omitempty"`
	Portrait bool     `json:"portrait,omitempty"`
	Status   bool     `json:"status,omitempty"`
}

const (
	usbVendor  = "04d9"
	usbProduct = "fd01"
)

// findDevice locates the LCD by USB vendor and product ID. USB bus and device
// numbers change across reboots and re-enumerations, so a fixed path is not
// reliable.
func findDevice() (string, error) {
	entries, err := os.ReadDir("/sys/bus/usb/devices")
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		base := "/sys/bus/usb/devices/" + entry.Name()
		vendor, err := os.ReadFile(base + "/idVendor")
		if err != nil {
			continue
		}
		product, err := os.ReadFile(base + "/idProduct")
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(vendor)) != usbVendor ||
			strings.TrimSpace(string(product)) != usbProduct {
			continue
		}
		bus, err := os.ReadFile(base + "/busnum")
		if err != nil {
			return "", err
		}
		dev, err := os.ReadFile(base + "/devnum")
		if err != nil {
			return "", err
		}
		var busNum, devNum int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(bus)), "%d", &busNum); err != nil {
			return "", err
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(string(dev)), "%d", &devNum); err != nil {
			return "", err
		}
		return fmt.Sprintf("/dev/bus/usb/%03d/%03d", busNum, devNum), nil
	}
	return "", fmt.Errorf("USB device %s:%s not found", usbVendor, usbProduct)
}

func main() {
	daemon := flag.Bool("daemon", false, "run LCD service")
	device := flag.String("device", "", "usbfs path for LCD; empty auto-discovers 04d9:fd01")
	socket := flag.String("socket", defaultSocket, "Unix socket path")
	imagePath := flag.String("image", "", "image to show")
	rotate := flag.Int("rotate", 0, "rotate image clockwise: 0, 90, 180, or 270")
	portrait := flag.Bool("portrait", false, "use LCD portrait orientation")
	status := flag.Bool("status", false, "use fixed status text layout")
	var textLines textFlag
	flag.Var(&textLines, "text", "text line to show; repeat for more lines")
	flag.Parse()

	if *daemon {
		if *device == "" {
			found, err := findDevice()
			if err != nil {
				log.Fatal(err)
			}
			*device = found
			log.Printf("found LCD at %s", *device)
		}
		if err := runDaemon(*device, *socket); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *imagePath == "" && len(textLines) == 0 {
		flag.Usage()
		return
	}
	if *rotate != 0 && *rotate != 90 && *rotate != 180 && *rotate != 270 {
		log.Fatal("-rotate must be 0, 90, 180, or 270")
	}
	if err := sendCommand(*socket, command{Image: *imagePath, Text: textLines, Rotate: *rotate, Portrait: *portrait, Status: *status}); err != nil {
		log.Fatal(err)
	}
}

func runDaemon(device, socket string) error {
	l, err := openLCD(device)
	if err != nil {
		return err
	}
	defer l.close()
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socket, err)
	}
	defer func() { _ = os.Remove(socket); _ = listener.Close() }()
	if err := os.Chmod(socket, 0666); err != nil {
		return err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	// All device I/O happens on this goroutine. Commands arrive on cmds and
	// the heartbeat runs only while idle, so no USB transfers ever overlap.
	cmds := make(chan request)
	go serve(listener, cmds)

	if err := l.heartbeat(); err != nil {
		log.Printf("initial heartbeat: %v", err)
	}
	if err := refreshFrame(rgb565(textImage([]string{"ACEMAGIC S1", "READY"})), l.write); err != nil {
		log.Printf("initial frame: %v", err)
	}
	log.Printf("daemon ready on %s", socket)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			log.Printf("shutting down")
			return nil
		case req := <-cmds:
			if err := applyCommand(l, req.cmd); err != nil {
				log.Printf("command failed: %v", err)
				req.reply <- err
			} else {
				req.reply <- nil
			}
		case <-tick.C:
			if err := l.heartbeat(); err != nil {
				log.Printf("heartbeat: %v", err)
			}
		}
	}
}

// request carries a decoded command and its reply channel to the daemon loop.
type request struct {
	cmd   command
	reply chan error
}

// serve accepts socket connections and forwards decoded commands to cmds.
// Each connection is handled in its own goroutine so a slow client cannot
// stall acceptance. The reply is sent only after the frame is written.
func serve(listener net.Listener, cmds chan<- request) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			var cmd command
			if err := json.NewDecoder(conn).Decode(&cmd); err != nil {
				_, _ = io.WriteString(conn, "read command: "+err.Error()+"\n")
				return
			}
			reply := make(chan error, 1)
			cmds <- request{cmd: cmd, reply: reply}
			if err := <-reply; err != nil {
				_, _ = io.WriteString(conn, err.Error()+"\n")
				return
			}
			_, _ = io.WriteString(conn, "ok\n")
		}()
	}
}

// applyCommand renders and shows one command. It runs on the daemon loop and
// is therefore the only writer besides the heartbeat tick.
func applyCommand(l *lcd, cmd command) error {
	var img image.Image
	var err error
	if cmd.Image != "" {
		img, err = loadImage(cmd.Image)
	} else if len(cmd.Text) != 0 {
		if cmd.Status && (cmd.Rotate == 90 || cmd.Rotate == 270) {
			img = statusTextImage(cmd.Text, cmd.Rotate)
			cmd.Rotate = 0
		} else if cmd.Rotate == 90 || cmd.Rotate == 270 {
			img = rotatedTextImage(cmd.Text, cmd.Rotate)
			cmd.Rotate = 0
		} else {
			img = textImage(cmd.Text)
		}
	} else {
		return fmt.Errorf("command needs image or text")
	}
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	if cmd.Rotate != 0 {
		img = rotateImage(img, cmd.Rotate)
	}
	if err := l.orientation(cmd.Portrait); err != nil {
		return err
	}
	if err := refreshFrame(rgb565(img), l.write); err != nil {
		return err
	}
	log.Printf("frame sent (%s)", describeCommand(cmd))
	return nil
}

func describeCommand(cmd command) string {
	if cmd.Image != "" {
		return "image " + cmd.Image
	}
	return fmt.Sprintf("text %q", strings.Join(cmd.Text, " / "))
}

func sendCommand(socket string, cmd command) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", socket, err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(cmd); err != nil {
		return err
	}
	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(response) != "ok" {
		return fmt.Errorf("service: %s", strings.TrimSpace(response))
	}
	return nil
}

type textFlag []string

func (t *textFlag) String() string { return strings.Join(*t, ",") }
func (t *textFlag) Set(value string) error {
	*t = append(*t, value)
	return nil
}
