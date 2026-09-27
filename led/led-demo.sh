#!/bin/sh
set -eu

cd "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

bin=./s1led
if [ ! -x "$bin" ]; then
	go build -o "$bin"
fi

for theme in 1 2 3 5 4; do
	printf 'LED theme %s\n' "$theme"
	sudo "$bin" -device /dev/serial/by-id/usb-1a86_USB_Serial-if00-port0 \
		-theme "$theme" -intensity 5 -speed 5
	sleep 3
done
