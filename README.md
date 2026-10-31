# hdmiview

Tools for cheap **MacroSilicon MS2109** USB HDMI capture dongles (USB ID `534d:2109`):

- **`hdmiview`**: watch and record the HDMI input in a web browser: live video,
  stereo audio, picture controls, video mode switching, snapshots and recording.
- **`ms2109tool`**: back up, inspect, patch and flash the dongle's EEPROM firmware
  over its HID interface.

Both are single Go binaries with no third-party Go dependencies. They run on
Linux, Windows and macOS.

## Platforms

|                    | Linux                     | Windows 64-bit               | macOS (Intel, Apple Silicon)    |
| ------------------ | ------------------------- | ---------------------------- | ------------------------------- |
| Video capture      | V4L2, direct              | ffmpeg (DirectShow)          | ffmpeg (AVFoundation)           |
| Video formats      | MJPEG, YUYV               | MJPEG, YUYV                  | Raw (encoded to JPEG by ffmpeg) |
| Audio capture      | ALSA (`arecord`)          | ffmpeg (DirectShow)          | ffmpeg (AVFoundation)           |
| Picture controls   | V4L2 controls             | Written to the chip over HID | Written to the chip over HID    |
| `ms2109tool` (HID) | `/dev/hidraw` (udev rule) | `hid.dll`, no driver needed  | IOKit                           |
| Tested on hardware | Yes                       | Not yet                      | Not yet                         |

On Windows and macOS the picture controls set the MS2109's brightness,
contrast, saturation and hue registers directly over HID, because ffmpeg
cannot change camera controls.

## Requirements

- Go 1.27 or newer to build
- Linux: V4L2 (`uvcvideo`), ALSA and `arecord` (package `alsa-utils`) for audio
- Windows and macOS: `ffmpeg` in `PATH` for capture (e.g. `winget install ffmpeg`,
  `brew install ffmpeg`). On macOS allow camera and microphone access for the
  terminal that runs `hdmiview` when asked.
- `ffmpeg` with `libx264` for recording (all platforms, optional)

## Download

Prebuilt binaries are on the
[releases page](https://github.com/jkandasa/hdmiview/releases):

- **`v*` releases**: tagged versions.
- **`devel`**: rolling pre-release, rebuilt from every commit on `main`.

Each tool has its own archive per platform:

| Platform                                                           | Archives                                                                       |
| ------------------------------------------------------------------ | ------------------------------------------------------------------------------ |
| Linux `amd64`, `arm64`, `arm` (ARMv7, e.g. Raspberry Pi OS 32-bit) | `<tool>-<version>-linux-<arch>.tar.gz`                                         |
| Windows 64-bit                                                     | `<tool>-<version>-windows-amd64.zip`                                           |
| macOS Intel and Apple Silicon                                      | `<tool>-<version>-darwin-amd64.tar.gz`, `<tool>-<version>-darwin-arm64.tar.gz` |

`<tool>` is `hdmiview` or `ms2109tool`; the Linux `ms2109tool` archive
includes the udev rule.

Each archive also contains this README and the licence. Check downloads
against `SHA256SUMS`.

## Build

```sh
make            # builds bin/hdmiview and bin/ms2109tool
make test
make dist       # Linux and Windows release archives in dist/
make dist DIST_TARGETS="darwin/amd64 darwin/arm64"   # on macOS (needs cgo)
```

The version comes from `git describe`. Check it with `--version`:

```
$ hdmiview --version
hdmiview v1.0.0
  commit:     1a2b3c4
  built:      2026-10-31T17:35:57Z
  go version: go1.27.0
  platform:   linux/amd64
```

## Releasing

- Every push to `main` deletes and recreates the `devel` pre-release, with its
  `devel` tag moved to the pushed commit.
- Pushing a tag such as `v1.0.0` creates a release for it, with notes since the
  previous `v*` tag. Tags with a suffix (`v1.1.0-rc1`) become pre-releases.

```sh
git tag v1.0.0 && git push origin v1.0.0
```

## Device access

On Windows and macOS no setup is needed. On Linux, `ms2109tool` talks to the
dongle through `/dev/hidrawN`, which is root-only by default. Install the udev rule to give the logged-in user access:

```sh
sudo cp udev/70-ms2109.rules /etc/udev/rules.d/
sudo udevadm control --reload && sudo udevadm trigger
```

Replug the device afterwards. The rule number must stay below 73 so systemd's
`uaccess` tag takes effect.

## hdmiview

```sh
./bin/hdmiview
```

Open <http://127.0.0.1:8080/>.

### Features

| Area      | What you get                                                                                                                                                                                                                                                                |
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Video     | MJPEG straight from the chip (no re-encoding), or uncompressed YUYV encoded to JPEG on the server. Format, resolution and frame rate are chosen from what the device reports. Fit / 1:1 / fullscreen.                                                                       |
| Picture   | Brightness, contrast, saturation and hue sliders with reset to the device default.                                                                                                                                                                                          |
| Audio     | 48 kHz stereo through Web Audio with about 50 to 250 ms latency. Volume up to 300%, mute, channel mode (stereo, swap, mono, left, right), latency setting and level meters. Audio starts automatically; if the browser blocks autoplay, the first click on the page starts it. |
| Recording | Server side with ffmpeg, so it keeps going when the page is closed. MP4 (H.264 + AAC) or MKV (original MJPEG + PCM). Audio and video stay in sync to within about 10 ms.                                                                                                    |
| Snapshots | Saved as JPEG on the server.                                                                                                                                                                                                                                                |
| Files     | Recordings and snapshots can be listed, downloaded and deleted from the page. Their folders can be changed from the page.                                                                                                                                                   |

The capture device is opened only while somebody is watching or recording, so
other programs (OBS, ffplay) can use it the rest of the time. Unplugging and
replugging is handled automatically.

### Flags

| Flag                                    | Default                            | Description                                                                          |
| --------------------------------------- | ---------------------------------- | ------------------------------------------------------------------------------------ |
| `--listen`                              | `127.0.0.1:8080`                   | HTTP address. Use `0.0.0.0:8080` to allow other machines on the network.             |
| `--video`                               | auto-detect                        | Linux: V4L2 device, e.g. `/dev/video0`. Windows/macOS: device name, e.g. `USB Video` |
| `--audio`                               | auto-detect                        | Linux: ALSA device. Windows/macOS: device name. `none` disables audio                |
| `--match`                               | `MS2109`                           | Device name used for auto-detection                                                  |
| `--format` `--width` `--height` `--fps` | `MJPG` 1920×1080 30                | Initial video mode. Overrides the saved mode.                                        |
| `--quality`                             | `90`                               | JPEG quality used for YUYV                                                           |
| `--recordings`                          | `~/Videos/hdmiview`                | Folder for recordings. Overrides the saved folder.                                   |
| `--snapshots`                           | `~/Pictures/hdmiview`              | Folder for snapshots. Overrides the saved folder.                                    |
| `--settings`                            | `~/.config/hdmiview/settings.json` | Where the video mode and folders are remembered. `""` disables saving.               |
| `--log-level`                           | `info`                             | `debug`, `info`, `warn` or `error`                                                   |
| `--log-format`                          | `console`                          | `console` (zap development style) or `json` (zap production style)                   |
| `--version`                             |                                    | Print version and exit                                                               |

Audio settings (volume, channel mode, latency) are stored in the browser.

### Security

The page shows whatever is on the captured screen, and it can save files on the
server. By default it only listens on localhost. Only expose it to a network you
trust.

- Requests that change anything must come from the page itself: other web sites
  open in the same browser cannot drive the API.
- The server only lists, serves or deletes files it created itself
  (`hdmi-*.mp4`, `hdmi-*.mkv`, `hdmi-*.jpg`, never symlinks).

### HTTP API

| Method & path                      | Purpose                                                                                                                                |
| ---------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /video`                       | MJPEG stream (`multipart/x-mixed-replace`)                                                                                             |
| `GET /audio`                       | Raw PCM stream, S16LE, 48 kHz, 2 channels                                                                                              |
| `GET /snapshot.jpg`                | Current frame                                                                                                                          |
| `POST /api/snapshot`               | Save a snapshot to the snapshots folder                                                                                                |
| `GET /api/snapshots`               | List snapshots                                                                                                                         |
| `GET`, `DELETE /snapshots/{name}`  | Get (`?download=1` forces download) or delete a snapshot                                                                               |
| `GET`, `POST /api/video`           | Current mode and supported modes; change the mode                                                                                      |
| `GET`, `POST /api/controls`        | Picture controls; set `{"id", "value"}` or `{"id", "reset": true}`                                                                     |
| `GET`, `POST /api/record`          | Recording status and list; `{"action": "start", "format": "mp4"\|"mkv", "quality": "high"\|"normal"\|"small"}` or `{"action": "stop"}` |
| `GET`, `DELETE /recordings/{name}` | Download or delete a recording                                                                                                         |
| `GET`, `POST /api/folders`         | Recording and snapshot folders                                                                                                         |
| `GET /stats`                       | Frame rate, bitrate, audio state, viewer count                                                                                         |

## ms2109tool

```
usage: ms2109tool [--dev /dev/hidrawN] [--verbose] <command> [args]

  info                           chip id, loaded header (XDATA) and EEPROM header
  backup [flags] <out.bin>       read EEPROM firmware to file (read twice and compared)
  inspect <img.bin>              validate an image file
  fixsum <in.bin> <out.bin>      recompute image checksums
  build-pro [flags] <stock.bin> <out.bin>
                                 apply the Pro-style patch to a stock image
  diff <a.bin> <b.bin>           list differing byte ranges
  edid [--extract f] <file>      decode the EDID of a firmware image or EDID file
  flash [flags] <img.bin>        write image (changed bytes only) and verify
  xread <addr> <len>             hexdump XDATA RAM
  xwrite <addr> <byte>...        write XDATA RAM (live, lost on replug)
  eread [--wide] <addr> <len>    hexdump EEPROM
  version                        print version
```

Run `ms2109tool <command> --help` for the flags of a command.

### Back up first

```sh
./bin/ms2109tool backup --xdata backups/original-xdata.bin backups/original-eeprom.bin
```

Keep that file somewhere safe. It is the only way back to the factory firmware.

### Patch and flash

```sh
./bin/ms2109tool build-pro backups/original-eeprom.bin firmware/ms2109-pro.bin
./bin/ms2109tool diff backups/original-eeprom.bin firmware/ms2109-pro.bin
./bin/ms2109tool flash --backup backups/preflash.bin firmware/ms2109-pro.bin
```

Replug the dongle to boot the new firmware. To go back:

```sh
./bin/ms2109tool flash backups/original-eeprom.bin
```

`flash` reads the EEPROM before writing and writes only the bytes that differ.
It invalidates the image header first and restores it last, so an interrupted
flash leaves the chip running from its mask ROM, where the HID interface still
works for another attempt. Afterwards it reads everything back to verify.

### What `build-pro` changes

Inspired by [MS2109-Pro](https://github.com/matiaspl/MS2109-Pro), but applied
to your own stock image instead of a fixed upstream baseline:

| Option             | Default      | Change                                                                                                                                                                     |
| ------------------ | ------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--framerates`     | on           | MJPEG 1920×1080, 1600×1200, 1360×768, 1280×1024 and 1280×960 advertise the real 30/25/20/10/5 fps from the datasheet instead of a fake 60 fps. 720p and below keep 60 fps. |
| `--stereo`         | on           | USB audio: stereo 48 kHz instead of mono 96 kHz                                                                                                                            |
| `--bt709`          | on           | UVC colour matching descriptor declares the BT.709 matrix                                                                                                                  |
| `--procamp`        | on           | Startup brightness −17, contrast 148, saturation 146, hue 0                                                                                                                |
| `--name`           | `MS2109-Pro` | USB product and interface name (`""` keeps stock)                                                                                                                          |
| `--chanorder`      | off          | MS2109-Pro's left/right routing change (`FE60=1`). It swapped the channels on the device this was tested with.                                                             |
| `--edid`           | keep         | Replace the EDID with a 128 or 256 byte file                                                                                                                               |
| `--edid-preferred` | keep         | Make a standard mode the EDID's preferred mode: `1080p60`, `1080p50`, `1080p30`, `1080p25`, `720p60`, `720p50`                                                             |

The builder refuses images that don't look like the expected stock firmware
(hook entry, frame table) and recomputes both checksums. Tests run the
generated hook in a small 8051 interpreter against a real RAM dump.

The MS2109 cannot output more than **1920×1080 at 30 fps** (see the
[datasheet](docs/MS2109-datasheet.pdf), in Chinese, section 6.4). That is a hardware
limit, so no firmware can add 1080p60 output.

### Custom EDID

The EDID is the data an HDMI source reads to decide what to send. The stock
EDID lives inside the firmware (EEPROM offset `0x182`, 256 bytes, enabled by
bit 4 of header byte 5). It calls itself "HDMI TO USB" and prefers
**1920×1080 at 60 Hz**, so most sources send 1080p60, which the chip can only
deliver at 30 fps.

Changing the preferred mode makes the source send what is actually captured:

```sh
./bin/ms2109tool edid backups/original-eeprom.bin            # show the current EDID
./bin/ms2109tool build-pro --edid-preferred 720p60 \
    backups/original-eeprom.bin firmware/ms2109-pro-720p60.bin
./bin/ms2109tool edid firmware/ms2109-pro-720p60.bin         # check before flashing
```

- `720p60`: the source sends 720p60, captured at a real 60 fps.
- `1080p30`: full resolution without frame-rate conversion in the chip.
  Tested on hardware: an Armbian (RK3128) box switched to 1080p30, and the
  MS2109 measured 1920×1080 at 29.99 Hz, 74.26 MHz.

Only the preferred timing changes; every other supported mode stays listed, so
the source can still be switched to them. To use a completely different EDID,
pass `--edid file.bin` (128 or 256 bytes; keep the CEA-861 extension block, or
HDMI sources fall back to DVI mode without audio). `ms2109tool edid --extract`
saves an EDID for editing.

After flashing, replug the dongle, then replug the HDMI cable or restart the
source so it reads the new EDID. On Linux sources, check with
`cat /sys/class/drm/*HDMI*/modes`.

### Recovery

| Situation                                                      | What to do                                                                                                                              |
| -------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| New firmware works badly, but the dongle still shows up on USB | `./bin/ms2109tool flash backups/original-eeprom.bin`, then replug                                                                       |
| A flash was interrupted                                        | `flash` invalidates the image header first, so the chip boots from its mask ROM only. The HID interface still works: run `flash` again. |
| The dongle no longer shows up on USB                           | Reprogram the EEPROM directly, see below                                                                                                |

The firmware lives in a **24C16** I²C EEPROM (2 KiB, SOIC-8, I²C addresses
`0x50` to `0x57`) next to the MS2109. Standard 24Cxx pinout: 4 GND, 5 SDA,
6 SCL, 7 WP, 8 VCC (pins 1 to 3 are unused on a 24C16).

**In circuit**, with a SOIC-8 test clip and a programmer that can run at 2.5 V
(e.g. Bus Pirate 5):

1. Unplug the dongle's USB cable.
2. Power the EEPROM at **2.5 V**. At 3.3 V the MS2109 is back-powered through
   the board and disturbs the I²C bus.
3. Tie **WP (pin 7) to ground**. The MS2109 normally drives it (GPIO5).
4. Write your 2048-byte backup (e.g. `backups/original-eeprom.bin`) as a 24C16
   and read it back to verify.

**Off board**: desolder the EEPROM, program it with any 24Cxx programmer (for
example a CH341A-based one at 3.3 V), verify, and solder it back.

These steps follow the recovery notes of
[kraln/macrosilicon_firmware](https://github.com/kraln/macrosilicon_firmware).

### How the firmware works

The MS2109 has an 8051 core that runs from mask ROM. At boot the ROM copies
extra code from an external I²C EEPROM (24C16, 2 KiB) into XDATA `0xCC00` and
calls it from fixed hooks.

EEPROM layout:

| Offset         | Content                                                                      |
| -------------- | ---------------------------------------------------------------------------- |
| `0x00`         | Magic `A5 5A` (8-bit addressed EEPROM) or `96 69` (16-bit)                   |
| `0x02`         | Code length N (big endian)                                                   |
| `0x04`         | Hook enable bits                                                             |
| `0x05`         | Bit 4: use the EDID from the firmware instead of the ROM's                   |
| `0x10`, `0x20` | Video and audio name records                                                 |
| `0x30`         | Code, N bytes, loaded to XDATA `0xCC00` (the EDID is at code offset `0x152`) |
| `0x30+N`       | 16-bit sum of bytes `0x02..0x2F`, then 16-bit sum of the code                |

HID protocol: 9-byte feature reports. `0xB5`/`0xB6` read and write XDATA,
`0xE5`/`0xE6` read and write the EEPROM.

## Credits

- [BertoldVdb/ms-tools](https://github.com/BertoldVdb/ms-tools): the MS2109 HID protocol and memory map
- [matiaspl/MS2109-Pro](https://github.com/matiaspl/MS2109-Pro): the firmware improvements `build-pro` is based on
- [Forest178/ms21xx-firmware-research](https://github.com/Forest178/ms21xx-firmware-research): EEPROM layout and checksums
- [kraln/macrosilicon_firmware](https://github.com/kraln/macrosilicon_firmware): EDID location and EEPROM recovery notes

## Disclaimer

Flashing firmware can make a device unusable. Back up first, and use this at
your own risk.

`backups/` and `firmware/` hold files from the device this was developed on.
`backups/` contains the stock EEPROM
firmware (`original-eeprom.bin`, version 20072901), a 64 KiB XDATA RAM dump
taken while it was running, and the EEPROM contents saved before each flash.
`firmware/` contains the images built from it with `build-pro`:
`ms2109-pro-1080p30.bin` is the one in use (preferred EDID mode 1080p30),
`ms2109-pro-nochan.bin` is the same without the EDID change, and
`ms2109-pro-mine.bin` is the earlier build with swapped audio channels. The tests use these files. They are only
known to match that one device, so always back up your own dongle before
flashing.

## License

[Apache License 2.0](LICENSE). The datasheet in `docs/` is MacroSilicon's
document and is not covered by this licence.
