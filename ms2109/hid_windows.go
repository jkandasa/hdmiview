package ms2109

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Windows: SetupAPI to enumerate HID interfaces, hid.dll for feature
// reports. Pure Go, no cgo.

var (
	modHID      = syscall.NewLazyDLL("hid.dll")
	modSetupAPI = syscall.NewLazyDLL("setupapi.dll")

	procHidDGetHidGuid          = modHID.NewProc("HidD_GetHidGuid")
	procHidDGetAttributes       = modHID.NewProc("HidD_GetAttributes")
	procHidDSetFeature          = modHID.NewProc("HidD_SetFeature")
	procHidDGetFeature          = modHID.NewProc("HidD_GetFeature")
	procHidDGetPreparsedData    = modHID.NewProc("HidD_GetPreparsedData")
	procHidDFreePreparsedData   = modHID.NewProc("HidD_FreePreparsedData")
	procHidPGetCaps             = modHID.NewProc("HidP_GetCaps")
	procSetupDiGetClassDevsW    = modSetupAPI.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceIfaces = modSetupAPI.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetIfaceDetailW  = modSetupAPI.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyInfoList  = modSetupAPI.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10
	hidpStatusSuccess    = 0x00110000
)

type guid struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

// SP_DEVICE_INTERFACE_DATA: DWORD cbSize; GUID; DWORD Flags; ULONG_PTR Reserved.
type spDeviceInterfaceData struct {
	cbSize   uint32
	guid     guid
	flags    uint32
	reserved uintptr
}

// HIDD_ATTRIBUTES: ULONG Size; USHORT VendorID, ProductID, VersionNumber (12 bytes).
type hiddAttributes struct {
	size                   uint32
	vendorID, productID, _ uint16
}

// HIDP_CAPS is 64 bytes; FeatureReportByteLength is the 5th USHORT.
type hidpCaps struct {
	usage, usagePage                uint16
	inputLen, outputLen, featureLen uint16
	_                               [17]uint16
	_                               [10]uint16
}

// hidDevicePaths lists the device paths of all present HID interfaces.
func hidDevicePaths() ([]string, error) {
	var g guid
	procHidDGetHidGuid.Call(uintptr(unsafe.Pointer(&g)))
	set, _, err := procSetupDiGetClassDevsW.Call(uintptr(unsafe.Pointer(&g)), 0, 0, digcfPresent|digcfDeviceInterface)
	if syscall.Handle(set) == syscall.InvalidHandle {
		return nil, fmt.Errorf("SetupDiGetClassDevs: %w", err)
	}
	defer procSetupDiDestroyInfoList.Call(set)

	// SP_DEVICE_INTERFACE_DETAIL_DATA_W: DWORD cbSize; WCHAR DevicePath[1].
	// cbSize is the size of the fixed part: 8 on 64-bit, 6 on 32-bit.
	detailSize := uint32(6)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		detailSize = 8
	}

	var paths []string
	for i := uintptr(0); ; i++ {
		ifd := spDeviceInterfaceData{cbSize: uint32(unsafe.Sizeof(spDeviceInterfaceData{}))}
		ok, _, _ := procSetupDiEnumDeviceIfaces.Call(set, 0, uintptr(unsafe.Pointer(&g)), i, uintptr(unsafe.Pointer(&ifd)))
		if ok == 0 {
			break
		}
		var need uint32
		procSetupDiGetIfaceDetailW.Call(set, uintptr(unsafe.Pointer(&ifd)), 0, 0, uintptr(unsafe.Pointer(&need)), 0)
		if need < detailSize+2 {
			continue
		}
		buf := make([]uint16, (need+1)/2)
		*(*uint32)(unsafe.Pointer(&buf[0])) = detailSize
		ok, _, _ = procSetupDiGetIfaceDetailW.Call(set, uintptr(unsafe.Pointer(&ifd)), uintptr(unsafe.Pointer(&buf[0])), uintptr(need), 0, 0)
		if ok == 0 {
			continue
		}
		paths = append(paths, syscall.UTF16ToString(buf[2:])) // DevicePath starts at byte 4
	}
	return paths, nil
}

type winHID struct {
	h          syscall.Handle
	featureLen int
}

// openWinHID opens path and checks that it is an MS2109 interface with a
// feature report of at least reportLen bytes.
func openWinHID(path string) (*winHID, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE)
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, share, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		// Some HID collections refuse read/write access; feature reports
		// still work on a handle opened without it.
		if h, err = syscall.CreateFile(p, 0, share, nil, syscall.OPEN_EXISTING, 0, 0); err != nil {
			return nil, err
		}
	}
	d := &winHID{h: h}

	attr := hiddAttributes{size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if ok, _, _ := procHidDGetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr))); ok == 0 {
		d.close()
		return nil, errors.New("HidD_GetAttributes failed")
	}
	if attr.vendorID != VID || attr.productID != PID {
		d.close()
		return nil, fmt.Errorf("not an MS2109 (%04x:%04x)", attr.vendorID, attr.productID)
	}

	var ppd uintptr
	if ok, _, _ := procHidDGetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&ppd))); ok == 0 {
		d.close()
		return nil, errors.New("HidD_GetPreparsedData failed")
	}
	var caps hidpCaps
	st, _, _ := procHidPGetCaps.Call(ppd, uintptr(unsafe.Pointer(&caps)))
	procHidDFreePreparsedData.Call(ppd)
	if st != hidpStatusSuccess {
		d.close()
		return nil, errors.New("HidP_GetCaps failed")
	}
	if int(caps.featureLen) < reportLen {
		d.close()
		return nil, fmt.Errorf("HID collection has no %d byte feature report", reportLen)
	}
	d.featureLen = int(caps.featureLen)
	return d, nil
}

func openTransport(path string) (hidTransport, string, error) {
	if path != "" {
		d, err := openWinHID(path)
		return d, path, err
	}
	paths, err := hidDevicePaths()
	if err != nil {
		return nil, "", err
	}
	for _, p := range paths {
		if d, err := openWinHID(p); err == nil {
			return d, p, nil
		}
	}
	return nil, "", errors.New("MS2109 (534d:2109) not found")
}

func (d *winHID) setFeature(buf []byte) error {
	b := make([]byte, d.featureLen)
	copy(b, buf)
	if ok, _, err := procHidDSetFeature.Call(uintptr(d.h), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b))); ok == 0 {
		return fmt.Errorf("HidD_SetFeature: %w", err)
	}
	return nil
}

func (d *winHID) getFeature(buf []byte) error {
	b := make([]byte, d.featureLen) // b[0] = report id 0
	if ok, _, err := procHidDGetFeature.Call(uintptr(d.h), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b))); ok == 0 {
		return fmt.Errorf("HidD_GetFeature: %w", err)
	}
	copy(buf, b)
	return nil
}

func (d *winHID) close() error { return syscall.CloseHandle(d.h) }
