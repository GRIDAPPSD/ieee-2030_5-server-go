package config

import "testing"

func TestExpandHome(t *testing.T) {
	tests := []struct {
		name    string
		home    string // $HOME for this case; "" leaves it undefined
		input   string
		want    string
		wantErr bool
	}{
		{name: "bare tilde expands to home", home: "/home/op", input: "~", want: "/home/op"},
		{name: "tilde slash expands and joins", home: "/home/op", input: "~/tls", want: "/home/op/tls"},
		{name: "tilde slash nested path", home: "/home/op", input: "~/a/b/c.pem", want: "/home/op/a/b/c.pem"},
		{name: "tilde with trailing slash collapses to home", home: "/home/op", input: "~/", want: "/home/op"},
		{name: "home undetermined fails rather than falling back to a relative path", home: "", input: "~/tls", wantErr: true},

		// These do not need HOME at all; setting it to "" proves the
		// function never resolves the home directory for a value with
		// no leading tilde.
		{name: "absolute path is unchanged with no HOME", home: "", input: "/etc/certs/ca.crt", want: "/etc/certs/ca.crt"},
		{name: "relative path is unchanged with no HOME", home: "", input: "certs/ca.crt", want: "certs/ca.crt"},
		{name: "empty string is unchanged with no HOME", home: "", input: "", want: ""},
		{name: "non-leading tilde is an ordinary character", home: "", input: "a~b/ca.crt", want: "a~b/ca.crt"},
		{name: "tilde mid-path segment is unchanged", home: "", input: "certs/~backup/ca.crt", want: "certs/~backup/ca.crt"},

		{name: "tilde-user form is rejected, not silently resolved", home: "/home/op", input: "~alice/tls", wantErr: true},
		{name: "bare tilde-user form is rejected", home: "/home/op", input: "~alice", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", tt.home)
			got, err := ExpandHome(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ExpandHome(%q) = %q, nil; want an error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandHome(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ExpandHome(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
