//go:build linux && (amd64 || arm64 || riscv64 || ppc64le || loong64)

package main

// 64-bit layouts of the ABI-dependent V4L2 structs.

const (
	sizeofV4L2Format = 208
	sizeofV4L2Buffer = 88
)

type v4l2Format struct {
	Type uint32
	_    uint32 // union is 8-byte aligned (it contains pointers)
	Pix  v4l2PixFormat
	_    [200 - 48]byte
}

type v4l2Buffer struct {
	Index, Type, BytesUsed, Flags, Field uint32
	_                                    uint32
	TsSec, TsUsec                        int64
	Timecode                             [16]byte
	Sequence, Memory                     uint32
	Offset                               uint32 // union m (low half on little endian)
	_                                    uint32
	Length, Reserved2, RequestFD         uint32
	_                                    uint32
}
