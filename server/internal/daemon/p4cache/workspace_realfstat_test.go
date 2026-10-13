package p4cache

import "testing"

// Captured byte-for-byte from `p4 -ztag fstat` on the DJ01 commit server
// (CRLF line endings, nested "... ..." lines for other clients' opens). The
// fake p4 used elsewhere never produces this shape.
const realFstatOpenedElsewhere = "... depotFile //DJ01_Proj/Dev/Game/DJ01/Source/CarbonEngine/Private/Attributes/CarbonAttributeData.cpp\r\n" +
	"... clientFile D:\\DJ\\Game\\DJ01\\Source\\CarbonEngine\\Private\\Attributes\\CarbonAttributeData.cpp\r\n" +
	"... isMapped \r\n" +
	"... headAction edit\r\n" +
	"... headType text\r\n" +
	"... headTime 1788496062\r\n" +
	"... headRev 2\r\n" +
	"... headChange 766932\r\n" +
	"... headModTime 1788446578\r\n" +
	"... pathSource //DJ01_Proj/Dev\r\n" +
	"... pathType share\r\n" +
	"... pathPermissions readonly\r\n" +
	"... effectiveComponentType none\r\n" +
	"... ... otherOpen0 frank@frank_HOST-B_Dev\r\n" +
	"... ... otherAction0 edit\r\n" +
	"... ... otherChange0 780652\r\n" +
	"... ... otherOpen 1\r\n" +
	"\r\n"

func TestFileStateParsesRealServerFstat(t *testing.T) {
	records := parseZtag(realFstatOpenedElsewhere)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1: %+v", len(records), records)
	}
	state := fileStateFromZtag(records[0])
	if state.DepotFile != "//DJ01_Proj/Dev/Game/DJ01/Source/CarbonEngine/Private/Attributes/CarbonAttributeData.cpp" {
		t.Fatalf("depotFile = %q", state.DepotFile)
	}
	if state.HeadType != "text" || state.HeadRev != 2 {
		t.Fatalf("headType=%q headRev=%d", state.HeadType, state.HeadRev)
	}
	// Not opened on this client: no action, no change.
	if state.OpenChange != "" {
		t.Fatalf("OpenChange = %q, want empty (not opened here)", state.OpenChange)
	}
	if len(state.OtherOpens) != 1 || state.OtherOpens[0] != "frank@frank_HOST-B_Dev" {
		t.Fatalf("OtherOpens = %v, want the other client's open", state.OtherOpens)
	}
	if state.OtherLock {
		t.Fatal("OtherLock set for a plain (non +l) open")
	}
}
