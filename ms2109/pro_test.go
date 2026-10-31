package ms2109

import (
	"bytes"
	"fmt"
	"os"
	"testing"
)

// mini8051 executes the small instruction subset the patch uses.
type mini8051 struct {
	x     []byte // XDATA (code at 0xCC00 lives here too)
	pc    int
	a     byte
	r7    byte
	dptr  int
	stack []int
	steps int
}

func (m *mini8051) run(stopAt int) error {
	for m.pc != stopAt {
		if m.steps++; m.steps > 10000 {
			return fmt.Errorf("runaway")
		}
		op := m.x[m.pc]
		switch op {
		case 0xEF:
			m.a = m.r7
			m.pc++
		case 0x74:
			m.a = m.x[m.pc+1]
			m.pc += 2
		case 0x90:
			m.dptr = int(m.x[m.pc+1])<<8 | int(m.x[m.pc+2])
			m.pc += 3
		case 0xA3:
			m.dptr = (m.dptr + 1) & 0xFFFF
			m.pc++
		case 0xE0:
			m.a = m.x[m.dptr]
			m.pc++
		case 0xF0:
			m.x[m.dptr] = m.a
			m.pc++
		case 0xB4:
			next := m.pc + 3
			if m.a != m.x[m.pc+1] {
				next += int(int8(m.x[m.pc+2]))
			}
			m.pc = next
		case 0x80:
			m.pc = m.pc + 2 + int(int8(m.x[m.pc+1]))
		case 0x12:
			m.stack = append(m.stack, m.pc+3)
			m.pc = int(m.x[m.pc+1])<<8 | int(m.x[m.pc+2])
		case 0x22:
			if len(m.stack) == 0 {
				return fmt.Errorf("stack underflow")
			}
			m.pc = m.stack[len(m.stack)-1]
			m.stack = m.stack[:len(m.stack)-1]
		default:
			return fmt.Errorf("unsupported opcode %02x at %04x", op, m.pc)
		}
	}
	return nil
}

func loadBackups(t *testing.T) ([]byte, []byte) {
	img, err := os.ReadFile("../backups/original-eeprom.bin")
	if err != nil {
		t.Skip("no backup image")
	}
	ram, err := os.ReadFile("../backups/original-xdata.bin")
	if err != nil {
		t.Skip("no xdata dump")
	}
	return img, ram
}

func TestBuildProEmulated(t *testing.T) {
	stock, ram := loadBackups(t)
	img, _, err := BuildPro(stock, DefaultProOptions())
	if err != nil {
		t.Fatal(err)
	}
	info, err := ParseImage(img)
	if err != nil || !info.ChecksumsOK() {
		t.Fatalf("bad output image: %v", err)
	}

	type exp struct {
		addr int
		want []byte
	}
	procamp := []exp{
		{0xC6A0, []byte{0xEF, 0xFF, 148, 0, 0, 0, 146, 0}},
		{0xFE90, []byte{0xEF, 148, 146, 0}},
	}
	desc := []exp{
		{0xC4C1, []byte{0x0B, 0x24, 0x02, 0x01, 0x02, 0x02, 0x10, 0x01, 0x80, 0xBB, 0x00}},
		{0xC2A4, []byte{0x06, 0x24, 0x0D, 0x01, 0x01, 0x01}},
		{0xC43B, []byte{0x06, 0x24, 0x0D, 0x01, 0x01, 0x01}},
	}

	for stage := 0; stage <= 0x15; stage++ {
		x := bytes.Clone(ram)
		copy(x[CodeLoadAddr:], img[HeaderLen:HeaderLen+info.CodeLen])
		m := &mini8051{x: x, pc: CodeLoadAddr, r7: byte(stage)}
		if err := m.run(CodeLoadAddr + 3); err != nil {
			t.Fatalf("stage %d: %v", stage, err)
		}
		if m.dptr != 0xDE0B || m.r7 != byte(stage) || len(m.stack) != 0 {
			t.Fatalf("stage %d: bad exit state dptr=%04x r7=%d stack=%v", stage, m.dptr, m.r7, m.stack)
		}

		var want []exp
		switch stage {
		case 1:
			want = append(procamp, desc...)
		case 2:
			want = desc
		}
		// every byte outside the expected writes (and the code area) is untouched
		expected := bytes.Clone(ram)
		copy(expected[CodeLoadAddr:], img[HeaderLen:HeaderLen+info.CodeLen])
		for _, e := range want {
			copy(expected[e.addr:], e.want)
		}
		if !bytes.Equal(x, expected) {
			for i := range x {
				if x[i] != expected[i] {
					t.Fatalf("stage %d: XDATA 0x%04x = %02x, want %02x", stage, i, x[i], expected[i])
				}
			}
		}
	}
}

func TestBuildProFrameTable(t *testing.T) {
	stock, _ := loadBackups(t)
	img, _, err := BuildPro(stock, DefaultProOptions())
	if err != nil {
		t.Fatal(err)
	}
	off := HeaderLen + frameTableAddr - CodeLoadAddr
	if !bytes.Equal(img[off:off+25], frameTableNew) {
		t.Fatal("frame table not patched")
	}
	// The stock hook entry is gone, so a second pass must be refused.
	_, _, err = BuildPro(img, DefaultProOptions())
	if err == nil {
		t.Fatal("re-patching an already patched image must fail")
	}
}
