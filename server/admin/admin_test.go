package admin

import "testing"

func TestWritebackGateMutualExclusion(t *testing.T) {
	a := &Admin{} // zero-value flags; ScanState nil = not scanning
	if !a.BeginWriteback() {
		t.Fatal("BeginWriteback should acquire when idle")
	}
	if a.canStartOrganize() {
		t.Error("organize must not start while writeback is running")
	}
	if a.canStartMatchRun() {
		t.Error("match must not start while writeback is running")
	}
	a.EndWriteback()
	if !a.canStartMatchRun() {
		t.Error("match should start once writeback ends")
	}
	// And the reverse: writeback yields while match holds the flag.
	// (canStartMatchRun above acquired matchRunning; BeginWriteback must refuse.)
	if a.BeginWriteback() {
		t.Error("writeback must not acquire while match is running")
	}
}
