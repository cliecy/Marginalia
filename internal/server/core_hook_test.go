package server

import (
	"errors"
	"testing"

	"marginalia/internal/config"
)

// TestOnChangeReportsSettingsChanges checks the hook the desktop app uses to
// follow the theme: it runs after Update and Apply with the settings before
// and after, not after a failed update, and not once removed.
func TestOnChangeReportsSettingsChanges(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	type change struct{ prev, next string }
	var got []change
	off := f.core.OnChange(func(prev, next config.Settings) {
		// The new settings are live when the hook runs.
		if live := f.core.Settings().Appearance.Theme; live != next.Appearance.Theme {
			t.Errorf("hook ran before the change was applied: live %q, next %q", live, next.Appearance.Theme)
		}
		got = append(got, change{prev.Appearance.Theme, next.Appearance.Theme})
	})

	// Through the HTTP route, as the settings page does.
	expect(t, f.app().do("PUT", "/api/app/settings", jsonMap{"appearance": jsonMap{"theme": "dark"}}), 200)
	// A failing update changes nothing and reports nothing.
	if _, err := f.core.Update(func(s *config.Settings) error { return errors.New("no") }); err == nil {
		t.Fatal("Update: want the error of fn")
	}
	// Apply reports too.
	next := f.core.Settings()
	next.Appearance.Theme = config.ThemeLight
	if err := f.core.Apply(next); err != nil {
		t.Fatal(err)
	}
	off()
	off() // removing twice is harmless
	next.Appearance.Theme = config.ThemeSystem
	if err := f.core.Apply(next); err != nil {
		t.Fatal(err)
	}

	want := []change{{"system", "dark"}, {"dark", "light"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("changes = %v, want %v", got, want)
	}
}

// TestOnChangeHookMayChangeSettings makes sure a hook can call back into the
// core without deadlocking.
func TestOnChangeHookMayChangeSettings(t *testing.T) {
	f := newFixture(t, func(o *fixtureOptions) { o.mode = modeDesktop })
	calls := 0
	f.core.OnChange(func(prev, next config.Settings) {
		calls++
		if next.Appearance.Theme == config.ThemeDark {
			if _, err := f.core.Update(func(s *config.Settings) error {
				s.Appearance.Theme = config.ThemeLight
				return nil
			}); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := f.core.Update(func(s *config.Settings) error {
		s.Appearance.Theme = config.ThemeDark
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || f.core.Settings().Appearance.Theme != config.ThemeLight {
		t.Fatalf("calls = %d, theme = %q", calls, f.core.Settings().Appearance.Theme)
	}
}
