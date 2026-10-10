package schemas

import "testing"

func TestAllEntriesCompileOffline(t *testing.T) {
	for _, name := range []string{"project", "tool", "agent", "service"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(name); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := Entry("unknown"); err == nil {
		t.Fatal("unknown schema accepted")
	}
}
