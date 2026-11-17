package identity

import "testing"

func TestParentInChecksWhatWindowsDoesNotGuarantee(t *testing.T) {
	table := []procRecord{
		{pid: 100, ppid: 50, started: 2000}, // agent CLI
		{pid: 50, ppid: 10, started: 1000},  // terminal
		{pid: 10, ppid: 4, started: 500},    // child of System
		{pid: 200, ppid: 77, started: 3000}, // parent 77 has exited
		{pid: 300, ppid: 60, started: 1500}, // parent id 60 was reused ...
		{pid: 60, ppid: 10, started: 2500},  // ... by a process that started later
		{pid: 400, ppid: 0, started: 100},   // child of the idle process
		{pid: 500, ppid: 500, started: 100}, // its own parent
		{pid: 600, ppid: 50, started: 0},    // start time unknown: no reuse check possible
		{pid: 700, ppid: 10, started: 900},  // a later child of a long-lived parent
	}
	tests := []struct {
		name string
		pid  int
		want int
		ok   bool
	}{
		{"live parent that started first", 100, 50, true},
		{"next step up", 50, 10, true},
		{"System is not an ancestor", 10, 0, false},
		{"exited parent ends the walk", 200, 0, false},
		{"reused parent id ends the walk", 300, 0, false},
		{"idle process is not an ancestor", 400, 0, false},
		{"self parent ends the walk", 500, 0, false},
		{"unknown start time trusts the snapshot", 600, 50, true},
		{"parent started before child", 700, 10, true},
		{"not in the table", 999, 0, false},
		{"zero pid", 0, 0, false},
		{"negative pid", -3, 0, false},
		{"pid beyond uint32", int(^uint32(0)) + 1, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parentIn(table, tc.pid)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parentIn(%d) = (%d, %v), want (%d, %v)", tc.pid, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// The ancestor walk over a Windows-shaped table stops where the table stops vouching, as it does on Linux when /proc
// cannot be read: the injected parentOf is the same resolver Windows uses.
func TestAncestorsOverAWindowsTable(t *testing.T) {
	table := []procRecord{
		{pid: 900, ppid: 100, started: 3000}, // mairc hook
		{pid: 100, ppid: 50, started: 2000},  // agent CLI
		{pid: 50, ppid: 10, started: 1000},   // terminal
		{pid: 10, ppid: 4, started: 500},     // child of System
	}
	parentOf := func(pid int) (int, bool) { return parentIn(table, pid) }
	got := Ancestors(900, 8, parentOf)
	want := []int{100, 50, 10}
	if len(got) != len(want) {
		t.Fatalf("Ancestors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Ancestors = %v, want %v", got, want)
		}
	}
}
