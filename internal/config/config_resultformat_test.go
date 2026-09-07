package config

import "testing"

func TestGetResultFormat(t *testing.T) {
	cases := []struct {
		name string
		cfg  *SSHConfig
		want string
	}{
		{"nil config defaults to text", nil, ResultFormatText},
		{"unset defaults to text", &SSHConfig{}, ResultFormatText},
		{"explicit text", &SSHConfig{ResultFormat: "text"}, ResultFormatText},
		{"json", &SSHConfig{ResultFormat: "json"}, ResultFormatJSON},
		{"json padded and mixed case", &SSHConfig{ResultFormat: "  JSON "}, ResultFormatJSON},
		{"unknown falls back to text", &SSHConfig{ResultFormat: "yaml"}, ResultFormatText},
	}
	for _, tc := range cases {
		if got := tc.cfg.GetResultFormat(); got != tc.want {
			t.Errorf("%s: GetResultFormat() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
