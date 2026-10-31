// ms2109tool backs up, inspects and flashes the EEPROM firmware of MS2109
// USB HDMI capture devices.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jkandasa/hdmiview/internal/cli"
	"github.com/jkandasa/hdmiview/ms2109"
)

const usage = `usage: ms2109tool [--dev /dev/hidrawN] [--verbose] <command> [args]

commands:
  info                           chip id, loaded header (XDATA) and EEPROM header
  backup [flags] <out.bin>       read EEPROM firmware to file (read-only, read twice + compared)
  inspect <img.bin>              validate an image file (no device needed)
  fixsum <in.bin> <out.bin>      recompute image checksums (no device needed)
  build-pro [flags] <stock.bin> <out.bin>
                                 apply the Pro-style patch to a stock image (no device needed)
  diff <a.bin> <b.bin>           list differing byte ranges (no device needed)
  flash [flags] <img.bin>        write image (changed bytes only) and verify
  xread <addr> <len>             hexdump XDATA RAM
  xwrite <addr> <byte>...        write XDATA RAM (live, lost on replug)
  eread [--wide] <addr> <len>    hexdump EEPROM
  version                        print version

Run "ms2109tool <command> --help" for the flags of a command.

global flags:
`

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", a...)
	os.Exit(1)
}

func num(s string) int {
	v, err := strconv.ParseInt(s, 0, 32)
	if err != nil {
		die("bad number %q", s)
	}
	return int(v)
}

func progress(total int) func(int) {
	return func(n int) {
		if n%256 < 5 || n == total {
			fmt.Fprintf(os.Stderr, "\r  %d/%d", n, total)
			if n == total {
				fmt.Fprintln(os.Stderr)
			}
		}
	}
}

// subFlags creates the flag set of a subcommand with "--flag" style help.
func subFlags(name, args string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: ms2109tool %s [flags] %s\n\nflags:\n", name, args)
		cli.PrintDefaults(os.Stderr, fs)
	}
	return fs
}

func main() {
	devPath := flag.String("dev", "", "hidraw device (default: auto-detect)")
	verbose := flag.Bool("verbose", false, "log HID traffic")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage)
		cli.PrintDefaults(os.Stderr, flag.CommandLine)
	}
	flag.Parse()
	if *showVersion {
		fmt.Println(cli.VersionString("ms2109tool"))
		return
	}
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	cmd, args := flag.Arg(0), flag.Args()[1:]

	switch cmd {
	case "version":
		fmt.Println(cli.VersionString("ms2109tool"))
		return
	case "help":
		flag.Usage()
		return
	case "inspect":
		cmdInspect(args)
		return
	case "fixsum":
		cmdFixsum(args)
		return
	case "build-pro":
		cmdBuildPro(args)
		return
	case "diff":
		cmdDiff(args)
		return
	}

	dev, err := ms2109.Open(*devPath)
	if err != nil {
		die("%v", err)
	}
	defer dev.Close()
	dev.Verbose = *verbose

	switch cmd {
	case "info":
		cmdInfo(dev)
	case "backup":
		cmdBackup(dev, args)
	case "flash":
		cmdFlash(dev, args)
	case "xread":
		cmdXread(dev, args)
	case "xwrite":
		cmdXwrite(dev, args)
	case "eread":
		cmdEread(dev, args)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func checkChip(dev *ms2109.Device) {
	id, err := dev.ChipID()
	if err != nil {
		die("read chip id: %v", err)
	}
	if id != ms2109.ChipIDMS2109 {
		die("chip id 0x%02x is not MS2109 (0x%02x)", id, ms2109.ChipIDMS2109)
	}
}

// detectLayout returns EEPROM size and addressing mode based on the stored magic.
func detectLayout(dev *ms2109.Device, size int) (int, bool) {
	head, err := dev.EEPROMRead(0, 2, false, nil)
	if err != nil {
		die("read EEPROM: %v", err)
	}
	wide := [2]byte{head[0], head[1]} == ms2109.Magic16Bit
	if size == 0 {
		size = 2048
		if wide {
			size = 8192
		}
	}
	return size, wide
}

// readTwice reads the EEPROM twice and fails if the reads differ.
func readTwice(dev *ms2109.Device, size int, wide bool) []byte {
	a, err := dev.EEPROMRead(0, size, wide, progress(size))
	if err != nil {
		die("read EEPROM: %v", err)
	}
	fmt.Fprintln(os.Stderr, "  second read for verification")
	b, err := dev.EEPROMRead(0, size, wide, progress(size))
	if err != nil {
		die("read EEPROM: %v", err)
	}
	if !bytes.Equal(a, b) {
		n, first := 0, -1
		for i := range a {
			if a[i] != b[i] {
				n++
				if first < 0 {
					first = i
				}
			}
		}
		die("two reads differ at %d offsets (first 0x%x)", n, first)
	}
	return a
}

func cmdInfo(dev *ms2109.Device) {
	id, err := dev.ChipID()
	if err != nil {
		die("%v", err)
	}
	name := "unexpected"
	if id == ms2109.ChipIDMS2109 {
		name = "MS2109"
	}
	fmt.Printf("device  : %s\nchip id : 0x%02x (%s)\n", dev.Path, id, name)
	uc, err := dev.XDATARead(ms2109.UserConfigAddr, ms2109.HeaderLen)
	if err != nil {
		die("%v", err)
	}
	fmt.Printf("XDATA 0x%04X (header copy loaded by ROM):\n", ms2109.UserConfigAddr)
	ms2109.Hexdump(os.Stdout, uc, ms2109.UserConfigAddr)
	head, err := dev.EEPROMRead(0, ms2109.HeaderLen, false, nil)
	if err != nil {
		die("%v", err)
	}
	fmt.Println("EEPROM 0x0000 header:")
	ms2109.Hexdump(os.Stdout, head, 0)
}

func cmdBackup(dev *ms2109.Device, args []string) {
	fs := subFlags("backup", "<out.bin>")
	size := fs.Int("size", 0, "EEPROM size in bytes (default: 2048, or 8192 for 16-bit EEPROMs)")
	xdata := fs.String("xdata", "", "also dump 64 KiB XDATA RAM to this file")
	overwrite := fs.Bool("overwrite", false, "overwrite existing output")
	fs.Parse(args)
	if fs.NArg() != 1 {
		die("backup needs an output file")
	}
	out := fs.Arg(0)
	if _, err := os.Stat(out); err == nil && !*overwrite {
		die("%s exists; use --overwrite", out)
	}

	checkChip(dev)
	sz, wide := detectLayout(dev, *size)
	fmt.Fprintf(os.Stderr, "reading %d bytes (wide=%v)\n", sz, wide)
	img := readTwice(dev, sz, wide)
	if wide && *size == 0 {
		// smaller 16-bit EEPROMs alias inside an 8K read
		for len(img) > 1024 && bytes.Equal(img[:len(img)/2], img[len(img)/2:]) {
			img = img[:len(img)/2]
		}
	}
	if err := os.WriteFile(out, img, 0o444); err != nil {
		die("%v", err)
	}
	fmt.Printf("saved %d bytes to %s\n", len(img), out)
	ms2109.PrintInfo(os.Stdout, out, img)

	if *xdata != "" {
		ram, err := dev.XDATARead(0, 0x10000)
		if err != nil {
			die("read XDATA: %v", err)
		}
		if err := os.WriteFile(*xdata, ram, 0o444); err != nil {
			die("%v", err)
		}
		fmt.Printf("saved XDATA to %s\n", *xdata)
	}
}

func cmdInspect(args []string) {
	if len(args) != 1 {
		die("inspect needs a file")
	}
	img, err := os.ReadFile(args[0])
	if err != nil {
		die("%v", err)
	}
	if !ms2109.PrintInfo(os.Stdout, args[0], img) {
		os.Exit(1)
	}
}

func cmdFixsum(args []string) {
	if len(args) != 2 {
		die("fixsum needs <in> <out>")
	}
	img, err := os.ReadFile(args[0])
	if err != nil {
		die("%v", err)
	}
	if err := ms2109.FixChecksums(img); err != nil {
		die("%v", err)
	}
	if err := os.WriteFile(args[1], img, 0o644); err != nil {
		die("%v", err)
	}
	ms2109.PrintInfo(os.Stdout, args[1], img)
}

func cmdFlash(dev *ms2109.Device, args []string) {
	fs := subFlags("flash", "<img.bin>")
	backup := fs.String("backup", "", "save current EEPROM contents here before writing")
	size := fs.Int("size", 0, "EEPROM size in bytes")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	force := fs.Bool("force", false, "skip image validity checks")
	fs.Parse(args)
	if fs.NArg() != 1 {
		die("flash needs an image file")
	}
	img, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		die("%v", err)
	}
	if !ms2109.PrintInfo(os.Stdout, fs.Arg(0), img) && !*force {
		die("image is invalid; refusing (use --force to override)")
	}

	checkChip(dev)
	sz, wide := detectLayout(dev, *size)
	if wide {
		// The ROM write command's addressing for 16-bit EEPROMs is unverified.
		die("16-bit EEPROM detected; ROM write path is not verified for it")
	}
	if len(img) > sz {
		die("image (%d bytes) larger than EEPROM (%d)", len(img), sz)
	}
	if info, err := ms2109.ParseImage(img); err == nil && info.Wide && !*force {
		die("image uses 16-bit magic but device EEPROM is 8-bit")
	}

	fmt.Fprintln(os.Stderr, "reading current EEPROM")
	cur := readTwice(dev, len(img), wide)
	if *backup != "" {
		if _, err := os.Stat(*backup); err == nil {
			die("%s exists; choose another name", *backup)
		}
		if err := os.WriteFile(*backup, cur, 0o444); err != nil {
			die("%v", err)
		}
		fmt.Printf("pre-flash backup saved to %s\n", *backup)
	}

	var diff []int
	for i := range img {
		if img[i] != cur[i] {
			diff = append(diff, i)
		}
	}
	fmt.Printf("%d bytes differ\n", len(diff))
	if len(diff) == 0 {
		return
	}
	if !*yes {
		fmt.Print("write EEPROM? type 'yes': ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "yes" {
			die("aborted")
		}
	}

	// Invalidate the magic first and restore it last: if the flash is
	// interrupted the ROM ignores the EEPROM and boots ROM-only, which still
	// exposes this HID interface so the device can be reflashed.
	if err := dev.EEPROMWriteByte(0, 0xFF); err != nil {
		die("write: %v", err)
	}
	if b, err := dev.EEPROMRead(0, 1, wide, nil); err != nil {
		die("read back: %v", err)
	} else if b[0] != 0xFF {
		die("EEPROM did not accept the write (write-protected?); nothing was changed")
	}
	order := make([]int, 0, len(diff)+1)
	for _, i := range diff {
		if i != 0 {
			order = append(order, i)
		}
	}
	order = append(order, 0)
	for n, i := range order {
		if err := dev.EEPROMWriteByte(i, img[i]); err != nil {
			die("write at 0x%x: %v", i, err)
		}
		if (n+1)%32 == 0 || n+1 == len(order) {
			fmt.Fprintf(os.Stderr, "\r  wrote %d/%d", n+1, len(order))
		}
	}
	fmt.Fprintln(os.Stderr)

	fmt.Fprintln(os.Stderr, "verifying")
	back, err := dev.EEPROMRead(0, len(img), wide, progress(len(img)))
	if err != nil {
		die("verify read: %v", err)
	}
	bad := 0
	for i := range img {
		if back[i] != img[i] {
			if bad == 0 {
				fmt.Fprintf(os.Stderr, "first mismatch at 0x%x: want %02x got %02x\n", i, img[i], back[i])
			}
			bad++
		}
	}
	if bad > 0 {
		die("VERIFY FAILED at %d bytes (EEPROM write-protected?); re-run flash or restore the backup", bad)
	}
	fmt.Println("flash OK - unplug and replug the device to boot the new firmware")
}

func cmdXread(dev *ms2109.Device, args []string) {
	if len(args) != 2 {
		die("xread <addr> <len>")
	}
	a := num(args[0])
	b, err := dev.XDATARead(a, num(args[1]))
	if err != nil {
		die("%v", err)
	}
	ms2109.Hexdump(os.Stdout, b, a)
}

func cmdEread(dev *ms2109.Device, args []string) {
	fs := subFlags("eread", "<addr> <len>")
	wide := fs.Bool("wide", false, "16-bit EEPROM addressing")
	fs.Parse(args)
	if fs.NArg() != 2 {
		die("eread [--wide] <addr> <len>")
	}
	a := num(fs.Arg(0))
	b, err := dev.EEPROMRead(a, num(fs.Arg(1)), *wide, nil)
	if err != nil {
		die("%v", err)
	}
	ms2109.Hexdump(os.Stdout, b, a)
}

func cmdBuildPro(args []string) {
	def := ms2109.DefaultProOptions()
	fs := subFlags("build-pro", "<stock.bin> <out.bin>")
	o := ms2109.ProOptions{}
	fs.BoolVar(&o.FrameRates, "framerates", def.FrameRates, "datasheet MJPEG frame rates (no fake 60 fps above 720p)")
	fs.BoolVar(&o.Procamp, "procamp", def.Procamp, "Pro startup brightness/contrast/saturation")
	fs.BoolVar(&o.Stereo48k, "stereo", def.Stereo48k, "stereo 48 kHz audio")
	fs.BoolVar(&o.ChannelOrder, "chanorder", def.ChannelOrder, "Pro L/R channel routing fix (FE60=1)")
	fs.BoolVar(&o.BT709, "bt709", def.BT709, "declare BT.709 colour matrix")
	fs.StringVar(&o.Name, "name", def.Name, "video/audio interface name (empty = keep stock)")
	fs.Parse(args)
	if fs.NArg() != 2 {
		die("build-pro [flags] <stock.bin> <out.bin>")
	}
	stock, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		die("%v", err)
	}
	if _, err := os.Stat(fs.Arg(1)); err == nil {
		die("%s exists", fs.Arg(1))
	}
	img, changes, err := ms2109.BuildPro(stock, o)
	if err != nil {
		die("%v", err)
	}
	if err := os.WriteFile(fs.Arg(1), img, 0o644); err != nil {
		die("%v", err)
	}
	fmt.Println("changes:")
	for _, c := range changes {
		fmt.Printf("  @0x%03x +%-3d %s\n", c.Offset, c.Len, c.What)
	}
	ms2109.PrintInfo(os.Stdout, fs.Arg(1), img)
}

func cmdDiff(args []string) {
	if len(args) != 2 {
		die("diff <a.bin> <b.bin>")
	}
	a, err := os.ReadFile(args[0])
	if err != nil {
		die("%v", err)
	}
	b, err := os.ReadFile(args[1])
	if err != nil {
		die("%v", err)
	}
	n := max(len(a), len(b))
	at := func(s []byte, i int) int {
		if i < len(s) {
			return int(s[i])
		}
		return -1
	}
	for i := 0; i < n; {
		if at(a, i) == at(b, i) {
			i++
			continue
		}
		j := i
		for j < n && at(a, j) != at(b, j) {
			j++
		}
		fmt.Printf("0x%03x-0x%03x (%d bytes)\n  - %x\n  + %x\n", i, j-1, j-i, a[i:min(j, len(a))], b[i:min(j, len(b))])
		i = j
	}
}

func cmdXwrite(dev *ms2109.Device, args []string) {
	if len(args) < 2 {
		die("xwrite <addr> <byte>...")
	}
	a := num(args[0])
	var data []byte
	for _, s := range args[1:] {
		v := num(s)
		if v < 0 || v > 255 {
			die("byte out of range: %s", s)
		}
		data = append(data, byte(v))
	}
	if err := dev.XDATAWrite(a, data); err != nil {
		die("%v", err)
	}
	b, err := dev.XDATARead(a, len(data))
	if err != nil {
		die("%v", err)
	}
	ms2109.Hexdump(os.Stdout, b, a)
}
