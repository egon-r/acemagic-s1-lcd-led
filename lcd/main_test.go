package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestWireReport(t *testing.T) {
	p := newReport(0xa1)
	p[3], p[4] = 0xf1, 1
	wire, err := wireReport(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != 4104 || !bytes.Equal(wire[:4], []byte{0x55, 0xa1, 0xf1, 1}) {
		t.Fatalf("bad wire report: length %d, header %x", len(wire), wire[:4])
	}
	for _, bad := range [][]byte{nil, make([]byte, 4104), append([]byte{1}, make([]byte, 4104)...)} {
		if _, err := wireReport(bad); err == nil {
			t.Fatal("accepted invalid report")
		}
	}
}

func TestRefreshFrame(t *testing.T) {
	pixels := make([]byte, width*height*2)
	for i := 0; i < width*height; i++ {
		binary.BigEndian.PutUint16(pixels[i*2:], uint16(i))
	}
	got := make([]byte, len(pixels))
	seen := make([]int, width*height)
	count := 0
	err := refreshFrame(pixels, func(p []byte) error {
		wire, err := wireReport(p)
		if err != nil {
			return err
		}
		if wire[0] != 0x55 || wire[1] != 0xa2 {
			t.Fatalf("bad refresh header: %x", wire[:8])
		}
		x, y := int(binary.LittleEndian.Uint16(wire[2:])), int(binary.LittleEndian.Uint16(wire[4:]))
		w, h := int(wire[6]), int(wire[7])
		if w == 0 || h == 0 || x+w > width || y+h > height || w*h*2 > dataLen {
			t.Fatalf("bad tile (%d,%d) %dx%d", x, y, w, h)
		}
		for row := 0; row < h; row++ {
			for col := 0; col < w; col++ {
				dst := (y+row)*width + x + col
				src := 8 + (row*w+col)*2
				copy(got[dst*2:dst*2+2], wire[src:src+2])
				seen[dst]++
			}
		}
		if !bytes.Equal(wire[8+w*h*2:], make([]byte, dataLen-w*h*2)) {
			t.Fatal("nonzero padding")
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 30 || !bytes.Equal(got, pixels) {
		t.Fatalf("frame mismatch; %d reports", count)
	}
	for i, n := range seen {
		if n != 1 {
			t.Fatalf("pixel %d sent %d times", i, n)
		}
	}
}

func TestRefreshErrors(t *testing.T) {
	calls := 0
	want := errors.New("write failed")
	send := func([]byte) error { calls++; return want }
	if err := refreshFrame(nil, send); err == nil || calls != 0 {
		t.Fatal("invalid frame reached writer")
	}
	if err := refreshFrame(make([]byte, width*height*2), send); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("write error not propagated: %v, calls %d", err, calls)
	}
}
