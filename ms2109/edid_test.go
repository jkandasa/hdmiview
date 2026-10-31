package ms2109

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func stockEDID(t *testing.T) (img, edid []byte) {
	img, err := os.ReadFile("../backups/original-eeprom.bin")
	if err != nil {
		t.Skip("no backup image")
	}
	e, err := ImageEDID(img)
	if err != nil {
		t.Fatal(err)
	}
	return img, e
}

func TestDecodeStockEDID(t *testing.T) {
	_, e := stockEDID(t)
	info, err := DecodeEDID(e)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "HDMI TO USB" || info.Manufacturer != "HJW" || info.Extensions != 1 {
		t.Errorf("unexpected EDID identity %+v", info)
	}
	if p := info.Preferred; p.HActive != 1920 || p.VActive != 1080 || p.PixelClockKHz != 148500 {
		t.Errorf("preferred = %v", p)
	}
}

// The stock EDID contains standard CEA timings: encoding them must
// reproduce the stock descriptor bytes exactly (bytes 0..11 are timing).
func TestEncodeDTDMatchesStock(t *testing.T) {
	_, e := stockEDID(t)
	for _, c := range []struct {
		mode string
		dtd  []byte
	}{
		{"1080p60", e[54:72]},          // base block, first descriptor
		{"720p60", e[128+44 : 128+62]}, // CEA extension, first descriptor
	} {
		got := make([]byte, 18)
		encodeDTD(got, StandardTimings[c.mode])
		if !bytes.Equal(got[:12], c.dtd[:12]) {
			t.Errorf("%s: encoded %x, stock %x", c.mode, got[:12], c.dtd[:12])
		}
	}
}

func TestDTDRoundTrip(t *testing.T) {
	for name, want := range StandardTimings {
		d := make([]byte, 18)
		encodeDTD(d, want)
		got, ok := decodeDTD(d)
		if !ok || got != want {
			t.Errorf("%s: round trip gave %+v", name, got)
		}
	}
}

func TestBuildProEDIDPreferred(t *testing.T) {
	stock, e := stockEDID(t)
	opt := DefaultProOptions()
	base, _, err := BuildPro(stock, opt)
	if err != nil {
		t.Fatal(err)
	}
	opt.EDIDPreferred = "720p60"
	img, _, err := BuildPro(stock, opt)
	if err != nil {
		t.Fatal(err)
	}
	ne, err := ImageEDID(img)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := DecodeEDID(ne)
	if p := info.Preferred; p.HActive != 1280 || p.VActive != 720 || p.RefreshHz() < 59.9 || p.RefreshHz() > 60.1 {
		t.Fatalf("preferred = %v", p)
	}
	// Only the first descriptor's timing bytes and the block checksum change
	// in the EDID; everything else matches the build without --edid-preferred.
	codeEnd := HeaderLen + int(binary.BigEndian.Uint16(img[2:])) // image checksums follow
	for i := range img {
		inDTD := i >= EDIDOffset+54 && i < EDIDOffset+66
		isSum := i == EDIDOffset+127
		if img[i] != base[i] && !inDTD && !isSum && i < codeEnd {
			t.Fatalf("unexpected change at 0x%x", i)
		}
	}
	if !bytes.Equal(ne[128:], e[128:]) {
		t.Error("extension block changed")
	}
	if info, err := ParseImage(img); err != nil || !info.ChecksumsOK() {
		t.Fatal("image checksums invalid")
	}
}

func TestBuildProEDIDReplace(t *testing.T) {
	stock, e := stockEDID(t)
	opt := DefaultProOptions()

	custom := bytes.Clone(e)
	copy(custom[54+18*3+5:], "MY CAPTURE  \n") // rename via the name descriptor
	fixEDIDChecksum(custom[:128])
	opt.EDID = custom
	img, _, err := BuildPro(stock, opt)
	if err != nil {
		t.Fatal(err)
	}
	ne, _ := ImageEDID(img)
	if info, _ := DecodeEDID(ne); info.Name != "MY CAPTURE" {
		t.Errorf("name = %q", info.Name)
	}

	bad := bytes.Clone(custom)
	bad[20]++ // checksum now wrong
	opt.EDID = bad
	if _, _, err := BuildPro(stock, opt); err == nil {
		t.Error("EDID with a bad checksum was accepted")
	}
	opt.EDID, opt.EDIDPreferred = nil, "1080p120"
	if _, _, err := BuildPro(stock, opt); err == nil {
		t.Error("unknown preferred mode was accepted")
	}
}
