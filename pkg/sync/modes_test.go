package sync

import "testing"

func TestModeValidationDiscardRejectedForPushModes(t *testing.T) {
	if err := ValidateModeOpts(ModeSync, ModeOpts{Discard: true}); err == nil {
		t.Error("--discard with sync should error")
	}
	if err := ValidateModeOpts(ModeAutosync, ModeOpts{Discard: true}); err == nil {
		t.Error("--discard with autosync should error")
	}
	if err := ValidateModeOpts(ModePull, ModeOpts{Discard: true}); err != nil {
		t.Errorf("--discard with pull should be allowed: %v", err)
	}
	if err := ValidateModeOpts(ModeMirror, ModeOpts{Discard: true}); err != nil {
		t.Errorf("--discard with mirror should be allowed: %v", err)
	}
}

func TestStrictOnlyForSync(t *testing.T) {
	if err := ValidateModeOpts(ModePull, ModeOpts{Strict: true}); err == nil {
		t.Error("--strict with pull should error (autosync/mirror similar)")
	}
	if err := ValidateModeOpts(ModeSync, ModeOpts{Strict: true}); err != nil {
		t.Errorf("--strict with sync should be allowed: %v", err)
	}
}
