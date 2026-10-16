package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// secretPath points SEP2_ADMIN_SECRET_FILE at a file inside a fresh temp
// dir and clears the key env, returning the path.
func secretPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sub", "admin.secret")
	t.Setenv("SEP2_ADMIN_SECRET_FILE", p)
	t.Setenv("SEP2_ADMIN_KEY", "")
	return p
}

func TestResolveAdminKeyEnvSetLeavesFileUntouched(t *testing.T) {
	p := secretPath(t)
	t.Setenv("SEP2_ADMIN_KEY", "an-env-key-of-16plus")

	got, err := resolveAdminKey(&certDirResolver{})
	if err != nil {
		t.Fatalf("resolveAdminKey: %v", err)
	}
	if got.Key != "an-env-key-of-16plus" || got.Source != "SEP2_ADMIN_KEY" || got.Generated {
		t.Fatalf("got %+v, want env key, source SEP2_ADMIN_KEY, not generated", got)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Fatalf("key file exists after an env-set start: Lstat err = %v", err)
	}
	if _, err := os.Lstat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Fatalf("key directory was created by an env-set start: %v", err)
	}
}

func TestResolveAdminKeyEnvSetDoesNotReadOrTouchExistingFile(t *testing.T) {
	p := secretPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	// World-readable on purpose: an env-set start must not even inspect it.
	if err := os.WriteFile(p, []byte("left-over-file-key-0123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEP2_ADMIN_KEY", "an-env-key-of-16plus")

	got, err := resolveAdminKey(&certDirResolver{})
	if err != nil {
		t.Fatalf("resolveAdminKey: %v", err)
	}
	if got.Key != "an-env-key-of-16plus" {
		t.Fatalf("Key = %q, want the env key", got.Key)
	}
	if !got.FileIgnored {
		t.Errorf("FileIgnored = false, want true so the start can log that the file is ignored")
	}
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("file mode = %o, want 644 unchanged", st.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if string(b) != "left-over-file-key-0123\n" {
		t.Errorf("file content changed: %q", b)
	}
}

func TestResolveAdminKeyEnvSetNeedsNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("SEP2_CERT_DIR", "")
	t.Setenv("SEP2_ADMIN_SECRET_FILE", "")
	t.Setenv("SEP2_ADMIN_KEY", "an-env-key-of-16plus")
	r := &certDirResolver{}
	if _, err := resolveAdminKey(r); err != nil {
		t.Fatalf("resolveAdminKey with env key and no HOME: %v", err)
	}
	if r.resolved {
		t.Fatal("the default certificate directory was resolved although the env key was set")
	}
}

func TestResolveAdminKeyCreatesFileWhenAbsent(t *testing.T) {
	p := secretPath(t)

	got, err := resolveAdminKey(&certDirResolver{})
	if err != nil {
		t.Fatalf("resolveAdminKey: %v", err)
	}
	if !hex64.MatchString(got.Key) {
		t.Fatalf("Key = %q, want 64 lowercase hex characters", got.Key)
	}
	if !got.Generated || got.Source != p {
		t.Fatalf("got %+v, want Generated and Source %q", got, p)
	}
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || !st.Mode().IsRegular() {
		t.Errorf("file mode = %v, want regular 0600", st.Mode())
	}
	dst, _ := os.Stat(filepath.Dir(p))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o, want 700", dst.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if string(b) != got.Key+"\n" {
		t.Errorf("file content = %q, want key plus one newline", b)
	}

	// A second start reads the same key and does not generate again.
	again, err := resolveAdminKey(&certDirResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Key != got.Key || again.Generated {
		t.Errorf("second start got %+v, want the same key, not generated", again)
	}
}

func TestResolveAdminKeyReadsExistingFileStrippingOneNewline(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"lf", "file-key-abcdefghij\n", "file-key-abcdefghij"},
		{"crlf", "file-key-abcdefghij\r\n", "file-key-abcdefghij"},
		{"no newline", "file-key-abcdefghij", "file-key-abcdefghij"},
		{"only one newline stripped", "file-key-abcdefghij\n\n", "file-key-abcdefghij\n"},
		{"inner and leading space kept", " file key abcdefghij \n", " file key abcdefghij "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := secretPath(t)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := resolveAdminKey(&certDirResolver{})
			if err != nil {
				t.Fatalf("resolveAdminKey: %v", err)
			}
			if got.Key != tc.want || got.Generated || got.Source != p {
				t.Fatalf("got %+v, want key %q from %s", got, tc.want, p)
			}
		})
	}
}

func TestResolveAdminKeyConcurrentFirstStartsAgree(t *testing.T) {
	secretPath(t)
	const n = 16
	var wg sync.WaitGroup
	results := make([]adminKeyResult, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = resolveAdminKey(&certDirResolver{})
		}(i)
	}
	close(start)
	wg.Wait()

	generated := 0
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("start %d: %v", i, errs[i])
		}
		if results[i].Key != results[0].Key {
			t.Fatalf("start %d key differs from start 0", i)
		}
		if results[i].Generated {
			generated++
		}
	}
	if generated != 1 {
		t.Errorf("%d starts report creating the file, want exactly 1", generated)
	}
	if !hex64.MatchString(results[0].Key) {
		t.Errorf("agreed key = %q, want 64 hex characters", results[0].Key)
	}
}

func TestResolveAdminKeyRefusesUnsafeFile(t *testing.T) {
	mk := func(t *testing.T, p string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("file-key-abcdefghij\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, p string)
		want  string
	}{
		{"group readable", func(t *testing.T, p string) { mk(t, p, 0o640) }, "chmod 600 "},
		{"other readable", func(t *testing.T, p string) { mk(t, p, 0o604) }, "chmod 600 "},
		{"group writable", func(t *testing.T, p string) { mk(t, p, 0o620) }, "chmod 600 "},
		{"symlink", func(t *testing.T, p string) {
			real := p + ".real"
			mk(t, real, 0o600)
			if err := os.Symlink(real, p); err != nil {
				t.Fatal(err)
			}
		}, "symbolic link"},
		{"directory", func(t *testing.T, p string) {
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "regular file"},
		{"other owner", func(t *testing.T, p string) {
			mk(t, p, 0o600)
			orig := currentUID
			currentUID = func() int { return os.Getuid() + 1 }
			t.Cleanup(func() { currentUID = orig })
		}, "not owned by the current user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := secretPath(t)
			tc.setup(t, p)
			_, err := resolveAdminKey(&certDirResolver{})
			if err == nil {
				t.Fatal("resolveAdminKey = nil error, want a refusal")
			}
			msg := err.Error()
			if !strings.Contains(msg, p) {
				t.Errorf("refusal does not name the path %s\n%s", p, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("refusal missing %q\n%s", tc.want, msg)
			}
			if strings.Contains(msg, "file-key-abcdefghij") {
				t.Errorf("refusal echoes the file content\n%s", msg)
			}
		})
	}
}

func TestResolveAdminKeyRefusesEmptyFile(t *testing.T) {
	p := secretPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAdminKey(&certDirResolver{})
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v, want a refusal naming %s and 'empty'", err, p)
	}
}

func TestReadAdminKeyCommand(t *testing.T) {
	t.Run("prints the key and never creates the file", func(t *testing.T) {
		p := secretPath(t)
		t.Setenv("SEP2_ADMIN_KEY", "ignored-env-key-0123456")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("file-key-abcdefghij\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := runAdminKey(&out, nil, &certDirResolver{}); err != nil {
			t.Fatalf("runAdminKey: %v", err)
		}
		if out.String() != "file-key-abcdefghij\n" {
			t.Errorf("stdout = %q, want the file key and one newline (env ignored)", out.String())
		}
	})
	t.Run("absent file is an error naming the path and creates nothing", func(t *testing.T) {
		p := secretPath(t)
		var out bytes.Buffer
		err := runAdminKey(&out, nil, &certDirResolver{})
		if err == nil || !strings.Contains(err.Error(), "no admin key at "+p) {
			t.Fatalf("err = %v, want 'no admin key at %s'", err, p)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want empty", out.String())
		}
		if _, statErr := os.Lstat(p); !os.IsNotExist(statErr) {
			t.Errorf("admin-key created the file: %v", statErr)
		}
		if _, statErr := os.Lstat(filepath.Dir(p)); !os.IsNotExist(statErr) {
			t.Errorf("admin-key created the directory: %v", statErr)
		}
	})
	t.Run("applies the same file checks as serve", func(t *testing.T) {
		p := secretPath(t)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("file-key-abcdefghij\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err := runAdminKey(&out, nil, &certDirResolver{})
		if err == nil || !strings.Contains(err.Error(), "chmod 600 "+p) {
			t.Fatalf("err = %v, want a refusal naming chmod 600 %s", err, p)
		}
		if out.Len() != 0 {
			t.Errorf("stdout = %q, want empty on refusal", out.String())
		}
	})
}

func TestApplyAdminKey(t *testing.T) {
	t.Run("no admin listener resolves and creates nothing", func(t *testing.T) {
		p := secretPath(t)
		cfg := &config.Config{}
		if err := applyAdminKey(cfg, &certDirResolver{}); err != nil {
			t.Fatal(err)
		}
		if cfg.AdminKey != "" || cfg.AdminKeySource != "" {
			t.Errorf("cfg touched: %+v", cfg)
		}
		if _, err := os.Lstat(filepath.Dir(p)); !os.IsNotExist(err) {
			t.Errorf("key dir created with no admin listener: %v", err)
		}
	})
	t.Run("deprecated SEP2_ADMIN_ADDR alias counts as a listener", func(t *testing.T) {
		p := secretPath(t)
		cfg := &config.Config{AdminAddr: ":8444"}
		if err := applyAdminKey(cfg, &certDirResolver{}); err != nil {
			t.Fatal(err)
		}
		if !hex64.MatchString(cfg.AdminKey) || cfg.AdminKeySource != p || !cfg.AdminKeyGenerated {
			t.Errorf("cfg = %+v, want generated key from %s", cfg, p)
		}
	})
	t.Run("env key wins and is recorded as the source", func(t *testing.T) {
		secretPath(t)
		t.Setenv("SEP2_ADMIN_KEY", "an-env-key-of-16plus")
		cfg := &config.Config{AdminListen: ":8444", AdminKey: "an-env-key-of-16plus"}
		if err := applyAdminKey(cfg, &certDirResolver{}); err != nil {
			t.Fatal(err)
		}
		if cfg.AdminKey != "an-env-key-of-16plus" || cfg.AdminKeySource != "SEP2_ADMIN_KEY" || cfg.AdminKeyGenerated {
			t.Errorf("cfg = %+v", cfg)
		}
	})
}
