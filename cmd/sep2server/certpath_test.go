package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCertDir(t *testing.T) {
	t.Run("no SEP2_CERT_DIR defaults to home/tls", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "")
		got, err := resolveCertDir()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "tls"); got != want {
			t.Fatalf("resolveCertDir() = %q, want %q", got, want)
		}
	})

	t.Run("explicit SEP2_CERT_DIR wins over the default", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "/mnt/keys")
		got, err := resolveCertDir()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/mnt/keys" {
			t.Fatalf("resolveCertDir() = %q, want /mnt/keys", got)
		}
	})

	t.Run("SEP2_CERT_DIR in tilde form is expanded", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "~/mytls")
		got, err := resolveCertDir()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "mytls"); got != want {
			t.Fatalf("resolveCertDir() = %q, want %q", got, want)
		}
	})

	t.Run("no SEP2_CERT_DIR and no HOME fails naming the setting", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Setenv("SEP2_CERT_DIR", "")
		_, err := resolveCertDir()
		if err == nil {
			t.Fatal("expected an error when the home directory cannot be determined")
		}
		if !strings.Contains(err.Error(), "SEP2_CERT_DIR") {
			t.Fatalf("error %q does not name SEP2_CERT_DIR as the setting to configure", err.Error())
		}
	})
}

func TestEnvPathOr(t *testing.T) {
	t.Run("unset env returns the fallback unchanged", func(t *testing.T) {
		t.Setenv("SEP2_TEST_PATH", "")
		got, err := envPathOr("SEP2_TEST_PATH", "/default/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/default/path" {
			t.Fatalf("envPathOr() = %q, want /default/path", got)
		}
	})

	t.Run("set env in tilde form is expanded", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_TEST_PATH", "~/x.pem")
		got, err := envPathOr("SEP2_TEST_PATH", "/default/path")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "x.pem"); got != want {
			t.Fatalf("envPathOr() = %q, want %q", got, want)
		}
	})

	t.Run("set env with ~user form fails naming the key", func(t *testing.T) {
		t.Setenv("SEP2_TEST_PATH", "~bob/x.pem")
		_, err := envPathOr("SEP2_TEST_PATH", "/default/path")
		if err == nil {
			t.Fatal("expected an error for the ~user form")
		}
		if !strings.Contains(err.Error(), "SEP2_TEST_PATH") {
			t.Fatalf("error %q does not name the setting SEP2_TEST_PATH", err.Error())
		}
	})
}

func TestExpandCSVPaths(t *testing.T) {
	t.Run("empty input yields nil", func(t *testing.T) {
		got, err := expandCSVPaths("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expandCSVPaths(\"\") = %v, want nil", got)
		}
	})

	t.Run("each entry is independently tilde-expanded", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		got, err := expandCSVPaths("~/roots/a.pem, /etc/roots/b.pem ,~/roots/c.pem")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{
			filepath.Join("/home/op", "roots/a.pem"),
			"/etc/roots/b.pem",
			filepath.Join("/home/op", "roots/c.pem"),
		}
		if len(got) != len(want) {
			t.Fatalf("expandCSVPaths() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("expandCSVPaths()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("a bad entry fails the whole list", func(t *testing.T) {
		_, err := expandCSVPaths("~alice/roots.pem")
		if err == nil {
			t.Fatal("expected an error for a ~user entry")
		}
	})
}

func TestResolveDir(t *testing.T) {
	t.Run("empty flag value falls back to the default cert dir", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "")
		got, err := resolveDir("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "tls"); got != want {
			t.Fatalf("resolveDir(\"\") = %q, want %q", got, want)
		}
	})

	t.Run("explicit flag value in tilde form is expanded", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		got, err := resolveDir("~/otherdir")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "otherdir"); got != want {
			t.Fatalf("resolveDir() = %q, want %q", got, want)
		}
	})

	t.Run("explicit absolute flag value is unchanged", func(t *testing.T) {
		got, err := resolveDir("/abs/dir")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/abs/dir" {
			t.Fatalf("resolveDir() = %q, want /abs/dir", got)
		}
	})
}

func TestResolveFile(t *testing.T) {
	t.Run("empty flag value derives name under the default cert dir", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "")
		got, err := resolveFile("", "ca.crt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "tls", "ca.crt"); got != want {
			t.Fatalf("resolveFile() = %q, want %q", got, want)
		}
	})

	t.Run("explicit flag value in tilde form is expanded, name ignored", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		got, err := resolveFile("~/other/ca.crt", "ca.crt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "other/ca.crt"); got != want {
			t.Fatalf("resolveFile() = %q, want %q", got, want)
		}
	})
}

func TestResolveOutAndCA(t *testing.T) {
	t.Run("all three flags empty derive from the default cert dir", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		t.Setenv("SEP2_CERT_DIR", "")
		out, ca, caKey, err := resolveOutAndCA("", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		wantDir := filepath.Join("/home/op", "tls")
		if out != wantDir {
			t.Fatalf("out = %q, want %q", out, wantDir)
		}
		if want := filepath.Join(wantDir, "ca.crt"); ca != want {
			t.Fatalf("ca = %q, want %q", ca, want)
		}
		if want := filepath.Join(wantDir, "ca.key"); caKey != want {
			t.Fatalf("caKey = %q, want %q", caKey, want)
		}
	})

	t.Run("each flag can be overridden independently, tilde expanded", func(t *testing.T) {
		t.Setenv("HOME", "/home/op")
		out, ca, caKey, err := resolveOutAndCA("~/out", "~/other-ca/ca.crt", "/abs/ca.key")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if want := filepath.Join("/home/op", "out"); out != want {
			t.Fatalf("out = %q, want %q", out, want)
		}
		if want := filepath.Join("/home/op", "other-ca/ca.crt"); ca != want {
			t.Fatalf("ca = %q, want %q", ca, want)
		}
		if caKey != "/abs/ca.key" {
			t.Fatalf("caKey = %q, want /abs/ca.key", caKey)
		}
	})
}
