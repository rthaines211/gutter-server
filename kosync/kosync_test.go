package kosync

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"testing"
)

// refPartialMD5 is an independent, literal port of KOReader's util.partialMD5
// (seek, read up to 1 KiB, stop when read returns nothing), using 32-bit
// shift semantics for the offsets.
func refPartialMD5(data []byte) string {
	lshift := func(x uint32, n int) uint32 { return x << (uint(n) & 31) }
	h := md5.New()
	for i := -1; i <= 10; i++ {
		off := int(lshift(1024, 2*i))
		if off >= len(data) {
			break
		}
		end := min(off+1024, len(data))
		h.Write(data[off:end])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestPartialMD5(t *testing.T) {
	gen := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte(i*31 + i/7)
		}
		return b
	}
	tests := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"tiny", 10},
		{"one sample", 1024},
		{"just past first", 1025},
		{"between offsets", 3000},
		{"exactly 4KiB", 4096},
		{"medium", 200_000},
		{"large", 5 << 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := gen(tc.size)
			got, err := PartialMD5(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if want := refPartialMD5(data); got != want {
				t.Errorf("PartialMD5 = %s, want %s", got, want)
			}
		})
	}
	// Known value: empty input hashes nothing.
	if got, _ := PartialMD5(bytes.NewReader(nil), 0); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("empty digest = %s", got)
	}
}

func TestSampleOffsets(t *testing.T) {
	offs := sampleOffsets()
	want := []int64{0, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864, 268435456, 1073741824}
	if len(offs) != len(want) {
		t.Fatalf("len = %d, want %d", len(offs), len(want))
	}
	for i := range want {
		if offs[i] != want[i] {
			t.Errorf("offs[%d] = %d, want %d", i, offs[i], want[i])
		}
	}
}

func TestFilenameDigestAndKey(t *testing.T) {
	if FilenameDigest("/lib/Saga/Saga 001.cbz") != FilenameDigest("Saga 001.cbz") {
		t.Error("filename digest should ignore directories")
	}
	if got := KeyFor("password"); got != "5f4dcc3b5aa765d61d8327deb882cf99" {
		t.Errorf("KeyFor = %s", got)
	}
}

func TestToGutterPage(t *testing.T) {
	tests := []struct {
		name     string
		progress string
		pct      float64
		total    int
		want     int
		ok       bool
	}{
		{"first page", "1", 0.04, 24, 0, true},
		{"middle", "12", 0.5, 24, 11, true},
		{"penultimate", "23", 0.958, 24, 22, true},
		{"last page finishes", "24", 1, 24, 24, true},
		{"past end clamps", "30", 1, 24, 24, true},
		{"whitespace", " 5 ", 0.2, 24, 4, true},
		{"xpointer uses percentage", "/body/DocFragment[3]/body/p[1]", 0.5, 24, 11, true},
		{"xpointer at end", "/body/x", 1, 24, 24, true},
		{"xpointer no percentage", "/body/x", 0, 24, 0, false},
		{"zero page", "0", 0, 24, 0, true},
		{"unknown total", "5", 0.2, 0, 0, false},
		{"single page book", "1", 1, 1, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ToGutterPage(tc.progress, tc.pct, tc.total)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ToGutterPage(%q, %v, %d) = %d, %v; want %d, %v",
					tc.progress, tc.pct, tc.total, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFromGutterPage(t *testing.T) {
	tests := []struct {
		cur, total int
		wantProg   string
		wantPct    float64
	}{
		{0, 24, "1", 0.0417},
		{11, 24, "12", 0.5},
		{23, 24, "24", 1},
		{24, 24, "24", 1}, // finished
		{-1, 24, "1", 0.0417},
		{3, 0, "1", 0},
	}
	for _, tc := range tests {
		prog, pct := FromGutterPage(tc.cur, tc.total)
		if prog != tc.wantProg || pct != tc.wantPct {
			t.Errorf("FromGutterPage(%d, %d) = %q, %v; want %q, %v", tc.cur, tc.total, prog, pct, tc.wantProg, tc.wantPct)
		}
	}
	// Round trip: every KOReader page survives Gutter and back.
	for p := 1; p <= 24; p++ {
		g, _ := ToGutterPage(itoa(p), 0, 24)
		back, _ := FromGutterPage(g, 24)
		if back != itoa(p) {
			t.Errorf("round trip page %d → %d → %s", p, g, back)
		}
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestValidate(t *testing.T) {
	ok := ProgressRequest{Document: "abc", Progress: "3", Percentage: 0.1, Device: "Kobo", DeviceID: "x"}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
	bad := []ProgressRequest{
		{Progress: "3", Device: "Kobo"},
		{Document: "a:b", Progress: "3", Device: "Kobo"},
		{Document: "abc", Device: "Kobo"},
		{Document: "abc", Progress: "3"},
		{Document: "abc", Progress: "3", Device: "Kobo", Percentage: 1.5},
	}
	for i, r := range bad {
		if r.Validate() == nil {
			t.Errorf("bad[%d] accepted", i)
		}
	}
}
