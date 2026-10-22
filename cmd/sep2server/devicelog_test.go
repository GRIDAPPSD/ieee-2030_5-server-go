package main

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestConfigFromEnvDeviceLogSettings(t *testing.T) {
	t.Setenv("SEP2_CERT_DIR", t.TempDir())
	t.Setenv("SEP2_DEVICE_LOG", "false")
	t.Setenv("SEP2_DEVICE_LOG_MAX_BYTES", "5000000")
	t.Setenv("SEP2_DEVICE_LOG_DIR", "/var/log/sep2devices")
	cfg, err := configFromEnv(&certDirResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DeviceLogOff || cfg.DeviceLogMaxBytes != 5_000_000 || cfg.DeviceLogDir != "/var/log/sep2devices" {
		t.Errorf("device log fields = off:%v max:%d dir:%q", cfg.DeviceLogOff, cfg.DeviceLogMaxBytes, cfg.DeviceLogDir)
	}

	t.Setenv("SEP2_DEVICE_LOG", "")
	t.Setenv("SEP2_DEVICE_LOG_MAX_BYTES", "")
	t.Setenv("SEP2_DEVICE_LOG_DIR", "")
	cfg, err = configFromEnv(&certDirResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceLogOff || cfg.DeviceLogMaxBytes != 0 || cfg.DeviceLogDir != "" {
		t.Errorf("unset device log fields = off:%v max:%d dir:%q; want on, 0, empty", cfg.DeviceLogOff, cfg.DeviceLogMaxBytes, cfg.DeviceLogDir)
	}
}

func TestConfigFromEnvRefusesBadDeviceLogSettings(t *testing.T) {
	t.Setenv("SEP2_CERT_DIR", t.TempDir())
	cases := []struct{ key, val string }{
		{"SEP2_DEVICE_LOG", "maybe"},
		{"SEP2_DEVICE_LOG", "FALSE"},
		{"SEP2_DEVICE_LOG_MAX_BYTES", "20"},
		{"SEP2_DEVICE_LOG_MAX_BYTES", "20000001"},
		{"SEP2_DEVICE_LOG_MAX_BYTES", "lots"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			cfg, err := configFromEnv(&certDirResolver{})
			if err == nil || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("configFromEnv = %v, %v; want a refusal naming %s", cfg, err, c.key)
			}
		})
	}
}

func deviceLogCfg(t *testing.T, certDir string) *config.Config {
	t.Helper()
	t.Setenv("SEP2_CERT_DIR", certDir)
	return &config.Config{KeyFile: filepath.Join(certDir, "server.key")}
}

func TestPrepareDeviceLogExplicitDirIsUsed(t *testing.T) {
	logs := captureLog(t)
	cfg := deviceLogCfg(t, t.TempDir())
	cfg.DeviceLogDir = filepath.Join(t.TempDir(), "devices")
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(cfg.DeviceLogDir); err != nil || !fi.IsDir() {
		t.Errorf("explicit directory not created: %v", err)
	}
	if cfg.DeviceLogOff || !strings.Contains(logs.String(), "device log directory ready: ") {
		t.Errorf("off=%v log=%q; want enabled line", cfg.DeviceLogOff, logs.String())
	}
}

func TestPrepareDeviceLogRefusesExplicitDirs(t *testing.T) {
	certDir := t.TempDir()
	base := t.TempDir()
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		dir  string
		want error
	}{
		{"uncreatable", filepath.Join(file, "sub"), devicelog.ErrDirUnusable},
		{"under the cert directory", filepath.Join(certDir, "logs"), devicelog.ErrDirProtected},
		{"the cert directory itself", certDir, devicelog.ErrDirProtected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := deviceLogCfg(t, certDir)
			cfg.DeviceLogDir = c.dir
			err := prepareDeviceLog(cfg, &certDirResolver{})
			if !errors.Is(err, c.want) || !strings.Contains(err.Error(), "SEP2_DEVICE_LOG_DIR") {
				t.Fatalf("err = %v; want %v naming SEP2_DEVICE_LOG_DIR", err, c.want)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(certDir, "logs")); err == nil {
		t.Error("a refused directory was created under the cert directory")
	}
}

func TestPrepareDeviceLogUnusableDefaultDisablesInsteadOfFailing(t *testing.T) {
	logs := captureLog(t)
	cwd := t.TempDir()
	// A file named etc makes the default ./etc/sep2/logs impossible to create.
	if err := os.WriteFile(filepath.Join(cwd, "etc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)
	cfg := deviceLogCfg(t, t.TempDir())
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatalf("an unusable default stopped startup: %v", err)
	}
	if !cfg.DeviceLogOff {
		t.Error("DeviceLogOff not set for an unusable default")
	}
	if !strings.Contains(logs.String(), "device log disabled: ./etc/sep2/logs") {
		t.Errorf("log = %q; want the disabled line naming the default directory", logs.String())
	}
}

func TestPrepareDeviceLogDefaultIsCreatedUnderWorkingDirectory(t *testing.T) {
	captureLog(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	cfg := deviceLogCfg(t, t.TempDir())
	cfg.DataDir = t.TempDir() // must not move the default
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(cwd, "etc", "sep2", "logs")); err != nil || !fi.IsDir() {
		t.Errorf("default directory not created under the working directory: %v", err)
	}
	if entries, _ := os.ReadDir(cfg.DataDir); len(entries) != 0 {
		t.Errorf("DataDir holds %d entries; the default must not follow it", len(entries))
	}
}

func TestPrepareDeviceLogOffTouchesNothing(t *testing.T) {
	logs := captureLog(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	cfg := deviceLogCfg(t, t.TempDir())
	cfg.DeviceLogOff = true
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Errorf("working directory holds %d entries after an off start", len(entries))
	}
	if !strings.Contains(logs.String(), "device log disabled") {
		t.Errorf("log = %q; want a disabled line", logs.String())
	}
}

func TestPrepareDeviceLogKeyInWorkingDirectoryKeepsDefaultLogOn(t *testing.T) {
	logs := captureLog(t)
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("SEP2_CERT_DIR", t.TempDir())
	cfg := &config.Config{KeyFile: "./server.key"}
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceLogOff || !strings.Contains(logs.String(), "device log directory ready: ") {
		t.Errorf("off=%v log=%q; a key in the working directory must not disable the default log", cfg.DeviceLogOff, logs.String())
	}
	if fi, err := os.Stat(filepath.Join(cwd, "etc", "sep2", "logs")); err != nil || !fi.IsDir() {
		t.Errorf("default directory not created: %v", err)
	}
}

func TestPrepareDeviceLogRefusesKeyFilePath(t *testing.T) {
	certDir := t.TempDir()
	keyDir := t.TempDir()
	cfg := deviceLogCfg(t, certDir)
	cfg.KeyFile = filepath.Join(keyDir, "server.key")
	cfg.DeviceLogDir = cfg.KeyFile
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err == nil {
		t.Fatal("a log directory equal to the key file path was accepted")
	}
}

func TestPrepareDeviceLogRefusesWhenCertDirCannotBeResolved(t *testing.T) {
	captureLog(t)
	t.Setenv("HOME", "")
	t.Setenv("SEP2_CERT_DIR", "")
	cfg := &config.Config{KeyFile: "/etc/tls/server.key"}
	cwd := t.TempDir()
	t.Chdir(cwd)
	err := prepareDeviceLog(cfg, &certDirResolver{})
	if err == nil || !strings.Contains(err.Error(), "certificate directory") {
		t.Fatalf("err = %v; want a refusal naming the certificate directory", err)
	}
	cfg.DeviceLogOff = true
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Errorf("an off log still resolved the cert directory: %v", err)
	}
}

func TestPrepareDeviceLogProtectsKeyFilePaths(t *testing.T) {
	captureLog(t)
	base := t.TempDir()
	for _, env := range []string{"SEP2_CA_KEY", "SEP2_SERVING_CA_KEY", "SEP2_DEVICE_CA_KEY", "SEP2_ADMIN_SECRET_FILE"} {
		t.Run(env, func(t *testing.T) {
			path := filepath.Join(base, env)
			t.Setenv("SEP2_CERT_DIR", t.TempDir())
			t.Setenv(env, path)
			cfg := &config.Config{KeyFile: filepath.Join(base, "server.key"), DeviceLogDir: path}
			if err := prepareDeviceLog(cfg, &certDirResolver{}); !errors.Is(err, devicelog.ErrDirProtected) {
				t.Fatalf("err = %v; want ErrDirProtected for a log directory equal to %s", err, env)
			}
		})
	}
	cfg := deviceLogCfg(t, t.TempDir())
	cfg.AdminKeyFile = filepath.Join(base, "admin.key")
	cfg.DeviceLogDir = cfg.AdminKeyFile
	if err := prepareDeviceLog(cfg, &certDirResolver{}); !errors.Is(err, devicelog.ErrDirProtected) {
		t.Errorf("admin key file: err = %v; want ErrDirProtected", err)
	}
}

func TestPrepareDeviceLogKeepsResolvedDir(t *testing.T) {
	captureLog(t)
	cfg := deviceLogCfg(t, t.TempDir())
	cfg.DeviceLogDir = filepath.Join(t.TempDir(), "d")
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(cfg.DeviceLogDir)
	if err != nil || cfg.DeviceLogResolvedDir != want {
		t.Errorf("DeviceLogResolvedDir = %q; want %q (%v)", cfg.DeviceLogResolvedDir, want, err)
	}
}

func TestPrepareDeviceLogStartsWithoutHomeWhenEveryCertPathIsExplicit(t *testing.T) {
	logs := captureLog(t)
	t.Setenv("HOME", "")
	t.Setenv("SEP2_CERT_DIR", "")
	t.Setenv("SEP2_CA_KEY", "/etc/tls/ca.key")
	cwd := t.TempDir()
	t.Chdir(cwd)
	cfg := &config.Config{KeyFile: "/etc/tls/server.key"}
	if err := prepareDeviceLog(cfg, &certDirResolver{}); err != nil {
		t.Fatalf("an unused, unresolvable default cert directory stopped startup: %v", err)
	}
	if cfg.DeviceLogOff || !strings.Contains(logs.String(), "device log directory ready: ") {
		t.Errorf("off=%v log=%q; want the log on", cfg.DeviceLogOff, logs.String())
	}
}

func TestPrepareDeviceLogExplicitCertDirStaysProtectedWithEveryPathExplicit(t *testing.T) {
	captureLog(t)
	certDir := t.TempDir()
	other := t.TempDir()
	t.Setenv("SEP2_CERT_DIR", certDir)
	t.Setenv("SEP2_CA_KEY", filepath.Join(other, "ca.key"))
	t.Setenv("SEP2_ADMIN_SECRET_FILE", filepath.Join(other, "admin.secret"))
	cfg := &config.Config{KeyFile: filepath.Join(other, "server.key"), DeviceLogDir: filepath.Join(certDir, "logs")}
	if err := prepareDeviceLog(cfg, &certDirResolver{}); !errors.Is(err, devicelog.ErrDirProtected) {
		t.Fatalf("err = %v; want ErrDirProtected for a log directory under the named SEP2_CERT_DIR", err)
	}
	if _, err := os.Stat(filepath.Join(certDir, "logs")); err == nil {
		t.Error("the refused directory was created")
	}
}
