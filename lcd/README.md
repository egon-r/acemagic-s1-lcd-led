# Acemagic S1 LCD controller

A small Linux-only Go program for the Acemagic S1 front LCD. It uses direct
usbfs ioctls and has no Go module dependencies.

## LCD

The front LCD is confirmed working.

- USB device: `04d9:fd01`
- USB interface: `1`
- Output endpoint: interrupt OUT `0x02`
- Size: 320×170 pixels
- Pixel format: RGB565, big endian
- Protocol report: 4,104 bytes: 8-byte header and up to 4,096 bytes of pixels

## Service

The system service owns the LCD, keeps its heartbeat active, and accepts local
commands over `/run/s1display.sock`.

```sh
cd lcd
go build -o s1display .
sudo install -m 0755 s1display /usr/local/bin/s1display
sudo install -m 0644 s1display.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now s1display
```

Show text:

```sh
s1display -text 'Hello' -text 'Acemagic S1'
```

Show an image:

```sh
s1display -image /path/to/image.png

# Rotate an image 90 degrees clockwise before it is shown.
s1display -image /path/to/image.png -rotate 90

# Ask the LCD firmware to use portrait orientation.
s1display -text 'Hello' -portrait
```

Images are resized to fill the 320×170 display. Use `-rotate 90`, `-rotate 180`,
or `-rotate 270` when needed. Supported formats are PNG,
JPEG, GIF, and any other image format registered by Go's standard library.

Use `-device` only with `-daemon` if the USB bus address changes:

```sh
sudo systemctl stop s1display
sudo s1display -daemon -device /dev/bus/usb/001/006
```

The service starts with a `READY` screen and keeps the display heartbeat active.
