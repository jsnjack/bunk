package main

import "testing"

// TestPresentCommandPassesArguments: "bunk present" leaves --hook, "-" and
// --help to RunPresent, which prints its own errors.
func TestPresentCommandPassesArguments(t *testing.T) {
	if !presentCmd.DisableFlagParsing || !presentCmd.SilenceErrors || presentCmd.RunE == nil {
		t.Fatal("present must disable flag parsing, leave error messages to RunPresent and use RunE")
	}
	if sub, _, err := rootCmd.Find([]string{"present", "--hook", "claude", "-"}); err != nil || sub != presentCmd {
		t.Fatalf("present --hook resolves to %v, %v", sub, err)
	}
}
