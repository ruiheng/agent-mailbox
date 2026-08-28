package launchpath

import "testing"

func TestResolveExecutableRejectsManagedWindowsChildWithoutLauncher(t *testing.T) {
	t.Parallel()

	_, err := resolveExecutable("", `C:\Users\alice\.local\lib\waypost\versions\v1\waypost.exe`, "windows")
	if err == nil {
		t.Fatal("resolveExecutable() error = nil, want managed version child rejection")
	}
}

func TestResolveExecutableAcceptsStableWindowsLauncher(t *testing.T) {
	t.Parallel()

	const executable = `C:\Users\alice\.local\bin\waypost.exe`
	got, err := resolveExecutable("", executable, "windows")
	if err != nil {
		t.Fatalf("resolveExecutable() error = %v", err)
	}
	if got != executable {
		t.Fatalf("resolveExecutable() = %q, want %q", got, executable)
	}
}

func TestResolveExecutableUsesPublishedStablePathForManagedWindowsChild(t *testing.T) {
	t.Parallel()

	const stable = `C:\Users\alice\.local\bin\waypost.exe`
	got, err := resolveExecutable(stable, `C:\Users\alice\.local\lib\waypost\versions\v1\waypost.exe`, "windows")
	if err != nil {
		t.Fatalf("resolveExecutable() error = %v", err)
	}
	if got != stable {
		t.Fatalf("resolveExecutable() = %q, want stable launcher %q", got, stable)
	}
}

func TestResolveExecutableDoesNotApplyWindowsLayoutRuleOnUnix(t *testing.T) {
	t.Parallel()

	const executable = "/opt/lib/waypost/versions/v1/waypost"
	got, err := resolveExecutable("", executable, "linux")
	if err != nil {
		t.Fatalf("resolveExecutable() error = %v", err)
	}
	if got != executable {
		t.Fatalf("resolveExecutable() = %q, want %q", got, executable)
	}
}
