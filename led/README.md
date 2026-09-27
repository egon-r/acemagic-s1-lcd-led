# Experimental Acemagic S1 LED controller

The LED utility is **unverified**: serial commands complete without errors, but no visible LED response has been observed. The cause is unknown; either the hardware or the code may be at fault. A successful serial write does not establish that the LEDs work.

The CH340 serial interface has USB ID `1a86:7523` and commonly appears as `/dev/ttyUSB0`. This program configures a 10,000-baud serial port and sends five bytes (`fa theme intensity speed checksum`) individually, with 5 ms delays. Theme, intensity, and speed accept values 1–5.

From the `led/` directory:

```sh
go build -o s1led .
sudo ./s1led -device /dev/ttyUSB0 -theme 1 -intensity 5 -speed 5
```

`./led-demo.sh` cycles through five themes with three-second pauses. Do not install it as a service until its behavior is verified.
