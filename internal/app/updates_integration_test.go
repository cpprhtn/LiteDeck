package app

import "testing"

// Against a real server that does not keep these files.
//
// The container has no update-notifier, which is the case worth pinning: the
// answer must be "I cannot tell", never "nothing to do". Debian behaves the
// same way, and answering zero there would be an invention with a number
// attached to it.
func TestUpdatesSayNothingWhereTheFilesAreAbsent(t *testing.T) {
	a := connectedApp(t)

	st, err := a.HostUpdates("fixture")
	if err != nil {
		t.Fatalf("HostUpdates: %v", err)
	}
	t.Logf("fixture: known=%v updates=%d security=%d reboot=%v pkgs=%v",
		st.Known, st.Updates, st.Security, st.RebootRequired, st.RebootPkgs)

	if st.Known {
		// If the image ever gains update-notifier this stops being the case
		// under test, and the message should say so rather than just failing.
		t.Skip("the fixture image now has update-notifier; this test covered its absence")
	}
	if st.Updates != -1 || st.Security != -1 {
		t.Errorf("counts came back as %d/%d on a server that keeps no counts — "+
			"unknown must not arrive as a number", st.Updates, st.Security)
	}
	if st.RebootRequired {
		t.Error("claimed a reboot is required on a server that does not record that")
	}
}
