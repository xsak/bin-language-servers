package json

import "testing"

func TestJSONC(t *testing.T) {
	// hasJSONCSuffix works on slash-separated paths, as produced by filepath.ToSlash on any
	// platform (Windows paths arrive with backslashes from the editor).
	tests := []struct {
		langID string
		path   string
		want   bool
	}{
		{"jsonc", "/x/y.json", true},
		{"json5", "/x/y.json", true},
		{"json", "/x/y.json", false},
		{"json", "/x/y.jsonc", true},
		{"json", "/home/me/tsconfig.json", true},
		{"json", "/home/me/project/.vscode/settings.json", true},
		{"json", "/home/me/.config/zed/settings.json", true},
		{"json", "/home/me/.config/gram/keymap.json", true},
		{"json", "/home/me/.eslintrc.json", true},
		{"json", "/home/me/.zed/settings.json", false},
		{"json", "C:/Users/me/project/.vscode/settings.json", true},
		{"json", "C:/Users/me/AppData/Roaming/gram/settings.json", true},
		{"json", "C:/Users/me/project/config.json", false},
		{"json", "C:/Users/me/package.json", false},
	}
	for _, tt := range tests {
		if got := jsonc(tt.langID, tt.path); got != tt.want {
			t.Errorf("jsonc(%q, %q) = %v, want %v", tt.langID, tt.path, got, tt.want)
		}
	}
}

func TestJSONCWindowsCase(t *testing.T) {
	// On Windows, editor directories keep their original case (%APPDATA%\Zed), so the match is
	// case-insensitive there.
	windowsPaths = true
	defer func() { windowsPaths = false }()
	tests := []struct {
		path string
		want bool
	}{
		{"C:/Users/me/AppData/Roaming/Zed/settings.json", true},
		{"C:/Users/me/AppData/Roaming/Zed/keymap.json", true},
		{"C:/Users/me/AppData/Roaming/GRAM/settings.json", true},
		{"C:/Users/me/project/config.json", false},
	}
	for _, tt := range tests {
		if got := jsonc("json", tt.path); got != tt.want {
			t.Errorf("jsonc with windowsPaths(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
