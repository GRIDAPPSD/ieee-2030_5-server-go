package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

const adminSecretName = "admin.secret"

// adminKeyResult is the resolved admin key and where it came from. Source is
// the env var name or the key file path, never the key.
type adminKeyResult struct {
	Key         string
	Source      string
	Generated   bool // the file was created by this call
	FileIgnored bool // env key used while a key file exists at Source's default path
	FilePath    string
}

// resolveAdminKey picks the admin key: SEP2_ADMIN_KEY if set, else the key
// file, created with a random key if absent. The cert directory is resolved
// only on the file path, so an env-keyed start needs no $HOME.
func resolveAdminKey(r *certDirResolver) (adminKeyResult, error) {
	if k := os.Getenv("SEP2_ADMIN_KEY"); k != "" {
		if err := auth.CheckAdminKey(k); err != nil {
			return adminKeyResult{}, fmt.Errorf("SEP2_ADMIN_KEY is not a usable admin key (%s): set it to a token of at least %d characters", unusableReason(err), auth.MinAdminKeyLength)
		}
		res := adminKeyResult{Key: k, Source: "SEP2_ADMIN_KEY"}
		// Lstat only: a stale file must be visible in the log, not read. A
		// throwaway resolver keeps an unresolvable $HOME from failing a start
		// that never needed the cert directory.
		if p, err := adminSecretPath(&certDirResolver{}); err == nil {
			if _, statErr := os.Lstat(p); statErr == nil {
				res.FileIgnored, res.FilePath = true, p
			}
		}
		return res, nil
	}
	p, err := adminSecretPath(r)
	if err != nil {
		return adminKeyResult{}, err
	}
	key, generated, err := loadOrCreateAdminKey(p)
	if err != nil {
		return adminKeyResult{}, err
	}
	return adminKeyResult{Key: key, Source: p, Generated: generated, FilePath: p}, nil
}

// applyAdminKey resolves the key into cfg when an admin listener is
// configured; with none, nothing is resolved, read or created.
func applyAdminKey(cfg *config.Config, r *certDirResolver) error {
	if cfg.EffectiveAdminListen() == "" {
		return nil
	}
	res, err := resolveAdminKey(r)
	if err != nil {
		return err
	}
	cfg.AdminKey = res.Key
	cfg.AdminKeySource = res.Source
	cfg.AdminKeyGenerated = res.Generated
	if res.FileIgnored {
		log.Printf("admin key from SEP2_ADMIN_KEY; %s exists and is ignored", res.FilePath)
	}
	return nil
}

// adminSecretPath is the one place the key file path is derived, shared by
// serve and the admin-key subcommand.
func adminSecretPath(r *certDirResolver) (string, error) {
	return r.envPathOrCertDir("SEP2_ADMIN_SECRET_FILE", adminSecretName)
}

// loadOrCreateAdminKey reads the key file, creating it with O_EXCL when
// absent. Losing the create race to another starter falls back to reading
// the winner's key.
func loadOrCreateAdminKey(path string) (key string, generated bool, err error) {
	if keyFileUnsupported != nil {
		return "", false, keyFileUnsupported
	}
	for attempt := 0; attempt < 2; attempt++ {
		key, err = readSettledAdminKeyFile(path)
		if err == nil {
			return key, false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
		key, err = createAdminKeyFile(path)
		if err == nil {
			return key, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", false, err
		}
	}
	return "", false, fmt.Errorf("admin key file %s: could not create or read it after a concurrent start", path)
}

var errEmptyKeyFile = errors.New("admin key file is empty")

// readSettledAdminKeyFile waits briefly for a file another starter has
// created (O_EXCL) but not yet written. A file that stays empty is refused,
// so a crash mid-write is told apart from a key and needs the file removed.
func readSettledAdminKeyFile(path string) (string, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		key, err := readAdminKeyFile(path)
		if !errors.Is(err, errEmptyKeyFile) || time.Now().After(deadline) {
			if errors.Is(err, errEmptyKeyFile) {
				return "", fmt.Errorf("admin key file %s is empty: remove it and restart to create a new key", path)
			}
			return key, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readAdminKeyFile opens the file once, without following a symlink, and
// checks the open descriptor before reading it: acting on the path twice would
// let a swap between the check and the read defeat the checks. A symlink,
// another owner's file, or a group/other-accessible file is refused, and so is
// a key that serve would refuse.
func readAdminKeyFile(path string) (string, error) {
	f, err := openKeyFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if errors.Is(err, errKeyFileSymlink) {
			return "", fmt.Errorf("admin key file %s is a symbolic link: replace it with a regular file (rm %s, then restart to create a new key)", path, path)
		}
		return "", fmt.Errorf("admin key file %s: %w", path, err)
	}
	// Read-only descriptor: a close failure loses nothing.
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("admin key file %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("admin key file %s is not a regular file", path)
	}
	owned, err := fileOwnedByCurrentUser(fi)
	if err != nil {
		return "", fmt.Errorf("admin key file %s: checking its owner: %w", path, err)
	}
	if !owned {
		return "", fmt.Errorf("admin key file %s is not owned by the current user: run it as the owner or remove the file to create a new key", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("admin key file %s is accessible to group or other (mode %04o): chmod 600 %s", path, fi.Mode().Perm(), path)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("admin key file %s: %w", path, err)
	}
	if len(b) == 0 {
		return "", errEmptyKeyFile
	}
	s := string(b)
	if strings.HasSuffix(s, "\r\n") {
		s = s[:len(s)-2]
	} else if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	if err := auth.CheckAdminKey(s); err != nil {
		return "", fmt.Errorf("admin key file %s does not hold a usable key (%s): remove it and restart to create a new key", path, unusableReason(err))
	}
	return s, nil
}

// unusableReason words an auth.CheckAdminKey failure without the key.
func unusableReason(err error) string {
	switch {
	case errors.Is(err, auth.ErrAdminKeyBlank):
		return "whitespace only"
	case errors.Is(err, auth.ErrAdminKeyShort):
		return fmt.Sprintf("shorter than %d characters", auth.MinAdminKeyLength)
	}
	return "empty"
}

// syncKeyFile is a variable so a test can fail the sync of a created file.
var syncKeyFile = func(f *os.File) error { return f.Sync() }

func createAdminKeyFile(path string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("admin key file %s: creating its directory: %w", path, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("admin key file %s: generating a key: %w", path, err)
	}
	key := hex.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", err
		}
		return "", fmt.Errorf("admin key file %s: %w", path, err)
	}
	// Past O_EXCL the file is ours: a failure below must not leave an empty
	// or partial file for the next start to adopt or trip over.
	fail := func(what string, err error) (string, error) {
		_ = f.Close()
		// Best effort: the write error is the one worth reporting.
		_ = os.Remove(path)
		return "", fmt.Errorf("admin key file %s: %s: %w", path, what, err)
	}
	if _, err := io.WriteString(f, key+"\n"); err != nil {
		return fail("writing", err)
	}
	if err := syncKeyFile(f); err != nil {
		return fail("sync", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("admin key file %s: close: %w", path, err)
	}
	return key, nil
}

const adminKeyUsage = `Usage: sep2server admin-key

Print the admin key from the key file (SEP2_ADMIN_SECRET_FILE, default <cert dir>/admin.secret).
SEP2_ADMIN_KEY is ignored. The file is never created here; the server creates it on first start.
`

// runAdminKey implements `sep2server admin-key`: read-only, so the running
// server is the only writer of the file.
func runAdminKey(out io.Writer, args []string, r *certDirResolver) error {
	fs := flag.NewFlagSet("admin-key", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		// A failed write of usage text to stderr has nowhere to be reported.
		_, _ = fmt.Fprint(fs.Output(), adminKeyUsage)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if keyFileUnsupported != nil {
		return keyFileUnsupported
	}
	p, err := adminSecretPath(r)
	if err != nil {
		return err
	}
	key, err := readSettledAdminKeyFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no admin key at %s: it is created the first time sep2server serve starts the admin listener", p)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, key)
	return err
}
