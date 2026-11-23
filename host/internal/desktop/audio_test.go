package desktop

import (
	"slices"
	"testing"
)

func TestLeftoverModules(t *testing.T) {
	list := "536870913\tmodule-always-sink\t\n" +
		"536870920\tmodule-null-sink\tsink_name=desklink sink_properties=device.description=DeskLink\n" +
		"536870921\tmodule-null-sink\tsink_name=desklinkother\n" +
		"536870922\tmodule-null-sink\tsink_properties=x sink_name=desklink\n" +
		"536870923\tmodule-loopback\tsink_name=desklink\n"
	got := leftoverModules(list)
	want := []string{"536870920", "536870922"}
	if !slices.Equal(got, want) {
		t.Errorf("leftoverModules = %v, want %v", got, want)
	}
}
