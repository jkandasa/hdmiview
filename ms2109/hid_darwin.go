//go:build darwin && cgo

package ms2109

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/hid/IOHIDManager.h>
#include <IOKit/hid/IOHIDDevice.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdint.h>
#include <stdlib.h>

static CFMutableDictionaryRef ms_matching(int vid, int pid) {
	CFMutableDictionaryRef d = CFDictionaryCreateMutable(kCFAllocatorDefault, 2,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFNumberRef v = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &vid);
	CFNumberRef p = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &pid);
	CFDictionarySetValue(d, CFSTR(kIOHIDVendorIDKey), v);
	CFDictionarySetValue(d, CFSTR(kIOHIDProductIDKey), p);
	CFRelease(v);
	CFRelease(p);
	return d;
}

// ms_open opens the first matching device. Returns 0 on success, 1 if no
// device matched, 2 if opening failed (IOReturn in *ioret). On success the
// caller owns *devOut and *mgrOut; on failure everything is released.
static int ms_open(int vid, int pid, IOHIDDeviceRef *devOut, IOHIDManagerRef *mgrOut, int *ioret) {
	*devOut = NULL;
	*mgrOut = NULL;
	IOHIDManagerRef mgr = IOHIDManagerCreate(kCFAllocatorDefault, kIOHIDOptionsTypeNone);
	CFMutableDictionaryRef match = ms_matching(vid, pid);
	IOHIDManagerSetDeviceMatching(mgr, match);
	CFRelease(match);
	CFSetRef set = IOHIDManagerCopyDevices(mgr);
	if (set == NULL || CFSetGetCount(set) == 0) {
		if (set) CFRelease(set);
		CFRelease(mgr);
		return 1;
	}
	CFIndex n = CFSetGetCount(set);
	const void **devs = (const void **)malloc(sizeof(void *) * n);
	CFSetGetValues(set, devs);
	IOHIDDeviceRef dev = (IOHIDDeviceRef)devs[0];
	CFRetain(dev);
	free(devs);
	CFRelease(set);
	IOReturn r = IOHIDDeviceOpen(dev, kIOHIDOptionsTypeNone);
	if (r != kIOReturnSuccess) {
		*ioret = (int)r;
		CFRelease(dev);
		CFRelease(mgr);
		return 2;
	}
	*devOut = dev;
	*mgrOut = mgr;
	return 0;
}

// The report helpers return 0 on success, otherwise the IOReturn code.
static int ms_set_feature(IOHIDDeviceRef dev, uint8_t *data, long len) {
	IOReturn r = IOHIDDeviceSetReport(dev, kIOHIDReportTypeFeature, 0, data, (CFIndex)len);
	return r == kIOReturnSuccess ? 0 : (int)r;
}

static int ms_get_feature(IOHIDDeviceRef dev, uint8_t *data, long *len) {
	CFIndex n = (CFIndex)*len;
	IOReturn r = IOHIDDeviceGetReport(dev, kIOHIDReportTypeFeature, 0, data, &n);
	*len = (long)n;
	return r == kIOReturnSuccess ? 0 : (int)r;
}

static void ms_close(IOHIDDeviceRef dev, IOHIDManagerRef mgr) {
	if (dev) {
		IOHIDDeviceClose(dev, kIOHIDOptionsTypeNone);
		CFRelease(dev);
	}
	if (mgr) CFRelease(mgr);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// macOS: IOKit HID. The device has no numbered reports, so report id 0 is
// passed separately and only the 8 data bytes travel in the buffer.

type darwinHID struct {
	dev C.IOHIDDeviceRef
	mgr C.IOHIDManagerRef
}

func openTransport(path string) (hidTransport, string, error) {
	var dev C.IOHIDDeviceRef
	var mgr C.IOHIDManagerRef
	var ioret C.int
	switch C.ms_open(C.int(VID), C.int(PID), &dev, &mgr, &ioret) {
	case 0:
		return &darwinHID{dev: dev, mgr: mgr}, "IOKit 534d:2109", nil
	case 1:
		return nil, "", fmt.Errorf("MS2109 (534d:2109) not found")
	default:
		return nil, "", fmt.Errorf("opening the MS2109 HID device failed (IOReturn 0x%x)", uint32(ioret))
	}
}

func (d *darwinHID) setFeature(buf []byte) error {
	data := buf[1:reportLen]
	if r := C.ms_set_feature(d.dev, (*C.uint8_t)(unsafe.Pointer(&data[0])), C.long(len(data))); r != 0 {
		return fmt.Errorf("IOHIDDeviceSetReport: IOReturn 0x%x", uint32(r))
	}
	return nil
}

func (d *darwinHID) getFeature(buf []byte) error {
	var data [reportLen - 1]byte
	n := C.long(len(data))
	if r := C.ms_get_feature(d.dev, (*C.uint8_t)(unsafe.Pointer(&data[0])), &n); r != 0 {
		return fmt.Errorf("IOHIDDeviceGetReport: IOReturn 0x%x", uint32(r))
	}
	buf[0] = 0
	copy(buf[1:], data[:min(int(n), len(data))])
	return nil
}

func (d *darwinHID) close() error {
	C.ms_close(d.dev, d.mgr)
	return nil
}
