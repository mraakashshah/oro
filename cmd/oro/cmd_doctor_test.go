package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDoctorCmdRejectsLegacyRecoverDoltArg(t *testing.T) {
	cmd := newDoctorCmd()
	cmd.SetArgs([]string{"recover-dolt"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err == nil {
		t.Fatal("oro doctor recover-dolt succeeded; want argument rejection")
	}
}

func TestHelpTextDoctorDoesNotAdvertiseDoltRepair(t *testing.T) {
	for _, forbidden := range []string{"corrupt Dolt", "Diagnose and repair oro installation issues"} {
		if strings.Contains(helpText, forbidden) {
			t.Fatalf("helpText still contains %q", forbidden)
		}
	}
	if !strings.Contains(helpText, "doctor     Diagnose oro installation issues") {
		t.Fatal("helpText does not describe doctor diagnostics")
	}
}
