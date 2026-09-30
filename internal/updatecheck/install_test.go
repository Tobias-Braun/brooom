package updatecheck

import "testing"

// Paths are synthetic so the table runs identically on every OS.
func TestDetectInstall(t *testing.T) {
	env := InstallEnv{GOBIN: "/opt/gobin", GOPATH: []string{"/home/u/mygo", "/extra"}, Home: "/home/u"}
	winEnv := InstallEnv{GOPATH: []string{`C:\Users\u\gopath`}, Home: `C:\Users\u`}
	tests := []struct {
		name string
		path string
		env  InstallEnv
		want string
	}{
		{"brew cellar arm", "/opt/homebrew/Cellar/brooom/1.0.0/bin/brooom", env, MethodBrew},
		{"brew cellar intel", "/usr/local/Cellar/brooom/1.0.0/bin/brooom", env, MethodBrew},
		{"brew caskroom intel", "/usr/local/Caskroom/brooom/1.0.0/brooom", env, MethodBrew},
		{"brew caskroom arm", "/opt/homebrew/Caskroom/brooom/1.0.0/brooom", env, MethodBrew},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Homebrew/bin/brooom", env, MethodBrew},
		{"scoop", `C:\Users\u\scoop\apps\brooom\current\brooom.exe`, winEnv, MethodScoop},
		{"scoop upper", `C:\Users\u\Scoop\Apps\brooom\1.0.0\brooom.exe`, winEnv, MethodScoop},
		{"gobin", "/opt/gobin/brooom", env, MethodGo},
		{"gopath bin", "/home/u/mygo/bin/brooom", env, MethodGo},
		{"second gopath entry", "/extra/bin/brooom", env, MethodGo},
		{"default go bin", "/home/u/go/bin/brooom", InstallEnv{Home: "/home/u"}, MethodGo},
		{"unrelated go/bin is not a go install", "/somewhere/go/bin/brooom", InstallEnv{}, MethodManual},
		{"go toolchain dir", "/usr/local/go/bin/brooom", InstallEnv{Home: "/home/u"}, MethodManual},
		{"windows gopath", `c:\users\u\gopath\bin\brooom.exe`, winEnv, MethodGo},
		{"windows go toolchain dir", `C:\Program Files\Go\bin\brooom.exe`, winEnv, MethodManual},
		{"manual usr local", "/usr/local/bin/brooom", env, MethodManual},
		{"manual windows", `C:\Tools\brooom.exe`, winEnv, MethodManual},
		{"prefix only is not under bin", "/opt/gobinary/brooom", env, MethodManual},
		{"empty path", "", env, MethodManual},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectInstall(tt.path, tt.env)
			if got.Method != tt.want {
				t.Fatalf("DetectInstall(%q).Method = %q, want %q", tt.path, got.Method, tt.want)
			}
			if got.Upgrade == "" {
				t.Error("empty upgrade instruction")
			}
		})
	}
}

func TestDetectInstallCommands(t *testing.T) {
	cases := map[string]string{
		"/opt/homebrew/Cellar/brooom/1/bin/brooom": "brew upgrade brooom",
		`C:\u\scoop\apps\brooom\brooom.exe`:        "scoop update brooom",
		"/home/u/go/bin/brooom":                    GoInstallCmd,
	}
	for path, want := range cases {
		got := DetectInstall(path, InstallEnv{Home: "/home/u"})
		if got.Upgrade != want {
			t.Errorf("%s: upgrade %q, want %q", path, got.Upgrade, want)
		}
		wantSuggestion := got.Method != MethodGo && !PackagesPublished
		if got.Suggestion != wantSuggestion {
			t.Errorf("%s: Suggestion = %v, want %v", path, got.Suggestion, wantSuggestion)
		}
	}
	manual := DetectInstall("/usr/local/bin/brooom", InstallEnv{})
	if manual.Suggestion || manual.Upgrade == "" {
		t.Errorf("manual install: %+v", manual)
	}
}
