package ms2109

import "fmt"

// asm is a tiny 8051 assembler for the handful of instructions the patches
// need. All branches are short (2-byte offsets) and resolved in finish().
type asm struct {
	org    int
	code   []byte
	labels map[string]int
	fixups []fixup
}

type fixup struct {
	at    int // index of the offset byte in code
	next  int // address of the following instruction
	label string
	abs   bool // 16-bit absolute address instead of rel8
}

func newAsm(org int) *asm { return &asm{org: org, labels: map[string]int{}} }

func (a *asm) pc() int { return a.org + len(a.code) }

func (a *asm) emit(b ...byte) { a.code = append(a.code, b...) }

func (a *asm) label(name string) { a.labels[name] = a.pc() }

func (a *asm) movDPTR(addr int) { a.emit(0x90, byte(addr>>8), byte(addr)) }
func (a *asm) movA(v byte)      { a.emit(0x74, v) }
func (a *asm) movxStore()       { a.emit(0xF0) } // MOVX @DPTR,A
func (a *asm) movxLoad()        { a.emit(0xE0) } // MOVX A,@DPTR
func (a *asm) incDPTR()         { a.emit(0xA3) }
func (a *asm) movAR7()          { a.emit(0xEF) }
func (a *asm) ret()             { a.emit(0x22) }

func (a *asm) cjneA(v byte, target string) {
	a.emit(0xB4, v, 0)
	a.fixups = append(a.fixups, fixup{len(a.code) - 1, a.pc(), target, false})
}

func (a *asm) sjmp(target string) {
	a.emit(0x80, 0)
	a.fixups = append(a.fixups, fixup{len(a.code) - 1, a.pc(), target, false})
}

func (a *asm) lcall(target string) {
	a.emit(0x12, 0, 0)
	a.fixups = append(a.fixups, fixup{len(a.code) - 2, a.pc(), target, true})
}

// store writes consecutive bytes to XDATA starting at addr.
func (a *asm) store(addr int, values ...byte) {
	a.movDPTR(addr)
	for i, v := range values {
		if i > 0 {
			a.incDPTR()
		}
		a.movA(v)
		a.movxStore()
	}
}

func (a *asm) finish() ([]byte, error) {
	for _, f := range a.fixups {
		t, ok := a.labels[f.label]
		if !ok {
			return nil, fmt.Errorf("undefined label %q", f.label)
		}
		if f.abs {
			a.code[f.at] = byte(t >> 8)
			a.code[f.at+1] = byte(t)
			continue
		}
		d := t - f.next
		if d < -128 || d > 127 {
			return nil, fmt.Errorf("branch to %q out of range (%d)", f.label, d)
		}
		a.code[f.at] = byte(int8(d))
	}
	return a.code, nil
}
