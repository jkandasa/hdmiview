//go:build linux && (arm || 386)

package main

// 32-bit layouts of the ABI-dependent V4L2 structs. The timestamp uses the
// 32-bit struct timeval (like Go's own syscalls on these platforms); the
// kernel accepts it through the *_TIME32 ioctl numbers implied by the size.

const (
	sizeofV4L2Format = 204
	sizeofV4L2Buffer = 68
)

type v4l2Format struct {
	Type uint32
	Pix  v4l2PixFormat // union is 4-byte aligned on 32-bit
	_    [200 - 48]byte
}

type v4l2Buffer struct {
	Index, Type, BytesUsed, Flags, Field uint32
	TsSec, TsUsec                        int32
	Timecode                             [16]byte
	Sequence, Memory                     uint32
	Offset                               uint32 // union m
	Length, Reserved2, RequestFD         uint32
}
