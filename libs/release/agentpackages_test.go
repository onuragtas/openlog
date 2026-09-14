package release

import "testing"

func TestLanguageAgentPackageNames(t *testing.T) {
	for in, want := range map[string]string{
		"0.9.1":          "0.9.1",
		"v0.9.1":         "0.9.1",
		"1.2.0-beta.3":   "1.2.0b3",
		"1.2.0-rc.1":     "1.2.0rc1",
		"1.2.0-alpha.12": "1.2.0a12",
		"1.2.0-beta":     "1.2.0-beta", // not convertible: unchanged, like release.yml
	} {
		if got := PythonVersion(in); got != want {
			t.Errorf("PythonVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NodeAgentPackageName("v0.9.1"); got != "openlog-node-0.9.1.tgz" {
		t.Errorf("node: %s", got)
	}
	if got := PythonAgentWheelName("1.2.0-beta.3"); got != "openlog_agent-1.2.0b3-py3-none-any.whl" {
		t.Errorf("wheel: %s", got)
	}
	if got := PythonAgentSdistName("0.9.1"); got != "openlog_agent-0.9.1.tar.gz" {
		t.Errorf("sdist: %s", got)
	}
	if got := DotnetAgentPackageName("1.2.0-beta.3"); got != "OpenLog.Agent.1.2.0-beta.3.nupkg" {
		t.Errorf("nupkg: %s", got)
	}
	if got := ComposeBundleName("v0.9.1-beta.1"); got != "openlog-compose-0.9.1-beta.1.tar.gz" {
		t.Errorf("compose bundle: %s", got)
	}
}
