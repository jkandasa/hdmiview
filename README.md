# hdmiview

Tools for cheap **MacroSilicon MS2109** USB HDMI capture dongles (USB ID `534d:2109`):

- **`hdmiview`** – watch and record the HDMI input in a web browser: live video,
  stereo audio, picture controls, video mode switching, snapshots and recording.
- **`ms2109tool`** – back up, inspect, patch and flash the dongle's EEPROM firmware
  over its HID interface.

Both are single Go binaries with no third-party Go dependencies. Linux only.

## Requirements

- Linux with V4L2 (`uvcvideo`) and ALSA
- Go 1.27 or newer to build
- `arecord` (package `alsa-utils`) for audio in `hdmiview`
- `ffmpeg` with `libx264` for recording in `hdmiview` (optional)

## Build

```sh
make            # builds bin/hdmiview and bin/ms2109tool
make test
```

The version comes from `git describe`; check it with `--version`.

## Device access

`ms2109tool` talks to the dongle through `/dev/hidrawN`, which is root-only by
default. Install the udev rule to give the logged-in user access:

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

| Area | What you get |
|---|---|
| Video | MJPEG straight from the chip (no re-encoding), or uncompressed YUYV encoded to JPEG on the server. Format, resolution and frame rate are chosen from what the device reports. Fit / 1:1 / fullscreen. |
| Picture | Brightness, contrast, saturation and hue sliders with reset to the device default. |
| Audio | 48 kHz stereo through Web Audio with about 50–250 ms latency. Volume up to 300%, mute, channel mode (stereo, swap, mono, left, right), latency setting and level meters. Audio starts automatically; if the browser blocks autoplay, the first click on the page starts it. |
| Recording | Server side with ffmpeg, so it keeps going when the page is closed. MP4 (H.264 + AAC) or MKV (original MJPEG + PCM). Audio and video stay in sync to within about 10 ms. |
| Snapshots | Saved as JPEG on the server. |
| Files | Recordings and snapshots can be listed, downloaded and deleted from the page. Their folders can be changed from the page. |

The capture device is opened only while somebody is watching or recording, so
other programs (OBS, ffplay) can use it the rest of the time. Unplugging and
replugging is handled automatically.

### Flags

| Flag | Default | Description |
|---|---|---|
| `--listen` | `127.0.0.1:8080` | HTTP address. Use `0.0.0.0:8080` to allow other machines on the network. |
| `--video` | auto-detect | V4L2 device, e.g. `/dev/video0` |
| `--audio` | auto-detect | ALSA device, or `none` to disable audio |
| `--match` | `MS2109` | Device name used for auto-detection |
| `--format` `--width` `--height` `--fps` | `MJPG` 1920×1080 30 | Initial video mode. Overrides the saved mode. |
| `--quality` | `90` | JPEG quality used for YUYV |
| `--recordings` | `~/Videos/hdmiview` | Folder for recordings. Overrides the saved folder. |
| `--snapshots` | `~/Pictures/hdmiview` | Folder for snapshots. Overrides the saved folder. |
| `--settings` | `~/.config/hdmiview/settings.json` | Where the video mode and folders are remembered. `""` disables saving. |
| `--log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `--log-format` | `console` | `console` (zap development style) or `json` (zap production style) |
| `--version` | | Print version and exit |

Audio settings (volume, channel mode, latency) are stored in the browser.

### Security

The page shows whatever is on the captured screen, and it can save files on the
server. By default it only listens on localhost. Only expose it to a network you
trust. The server only lists, serves or deletes files it created itself
(`hdmi-*.mp4`, `hdmi-*.mkv`, `hdmi-*.jpg`, never symlinks).

### HTTP API

| Method & path | Purpose |
|---|---|
| `GET /video` | MJPEG stream (`multipart/x-mixed-replace`) |
| `GET /audio` | Raw PCM stream, S16LE, 48 kHz, 2 channels |
| `GET /snapshot.jpg` | Current frame |
| `POST /api/snapshot` | Save a snapshot to the snapshots folder |
| `GET /api/snapshots` | List snapshots |
| `GET`, `DELETE /snapshots/{name}` | Get (`?download=1` forces download) or delete a snapshot |
| `GET`, `POST /api/video` | Current mode and supported modes; change the mode |
| `GET`, `POST /api/controls` | Picture controls; set `{"id", "value"}` or `{"id", "reset": true}` |
| `GET`, `POST /api/record` | Recording status and list; `{"action": "start", "format": "mp4"\|"mkv", "quality": "high"\|"normal"\|"small"}` or `{"action": "stop"}` |
| `GET`, `DELETE /recordings/{name}` | Download or delete a recording |
| `GET`, `POST /api/folders` | Recording and snapshot folders |
| `GET /stats` | Frame rate, bitrate, audio state, viewer count |

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

| Option | Default | Change |
|---|---|---|
| `--framerates` | on | MJPEG 1920×1080, 1600×1200, 1360×768, 1280×1024 and 1280×960 advertise the real 30/25/20/10/5 fps from the datasheet instead of a fake 60 fps. 720p and below keep 60 fps. |
| `--stereo` | on | USB audio: stereo 48 kHz instead of mono 96 kHz |
| `--bt709` | on | UVC colour matching descriptor declares the BT.709 matrix |
| `--procamp` | on | Startup brightness −17, contrast 148, saturation 146, hue 0 |
| `--name` | `MS2109-Pro` | USB product and interface name (`""` keeps stock) |
| `--chanorder` | off | MS2109-Pro's left/right routing change (`FE60=1`). It swapped the channels on the device this was tested with. |

The builder refuses images that don't look like the expected stock firmware
(hook entry, frame table) and recomputes both checksums. Tests run the
generated hook in a small 8051 interpreter against a real RAM dump.

The MS2109 cannot output more than **1920×1080 at 30 fps** (see the
[datasheet](docs/MS2109-datasheet.pdf), in Chinese, section 6.4). That is a hardware
limit, so no firmware can add 1080p60 output.

### How the firmware works

The MS2109 has an 8051 core that runs from mask ROM. At boot the ROM copies
extra code from an external I²C EEPROM (24C16, 2 KiB) into XDATA `0xCC00` and
calls it from fixed hooks.

EEPROM layout:

| Offset | Content |
|---|---|
| `0x00` | Magic `A5 5A` (8-bit addressed EEPROM) or `96 69` (16-bit) |
| `0x02` | Code length N (big endian) |
| `0x04` | Hook enable bits |
| `0x10`, `0x20` | Video and audio name records |
| `0x30` | Code, N bytes |
| `0x30+N` | 16-bit sum of bytes `0x02..0x2F`, then 16-bit sum of the code |

HID protocol: 9-byte feature reports. `0xB5`/`0xB6` read and write XDATA,
`0xE5`/`0xE6` read and write the EEPROM.

## Credits

- [BertoldVdb/ms-tools](https://github.com/BertoldVdb/ms-tools) – the MS2109 HID protocol and memory map
- [matiaspl/MS2109-Pro](https://github.com/matiaspl/MS2109-Pro) – the firmware improvements `build-pro` is based on
- [Forest178/ms21xx-firmware-research](https://github.com/Forest178/ms21xx-firmware-research) – EEPROM layout and checksums

## Disclaimer

Flashing firmware can make a device unusable. Back up first, and use this at
your own risk. No vendor firmware is included in this repository.
