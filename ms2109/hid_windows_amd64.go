package ms2109

import "unsafe"

// Compile-time checks of the Windows struct layouts on 64-bit
// (SP_DEVICE_INTERFACE_DATA 32, HIDD_ATTRIBUTES 12, HIDP_CAPS 64 bytes).
var (
	_ [unsafe.Sizeof(spDeviceInterfaceData{}) - 32]struct{}
	_ [32 - unsafe.Sizeof(spDeviceInterfaceData{})]struct{}
	_ [unsafe.Sizeof(hiddAttributes{}) - 12]struct{}
	_ [12 - unsafe.Sizeof(hiddAttributes{})]struct{}
	_ [unsafe.Sizeof(hidpCaps{}) - 64]struct{}
	_ [64 - unsafe.Sizeof(hidpCaps{})]struct{}
)
