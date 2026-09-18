package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// resolveCertDir returns the base directory for certificate files:
// SEP2_CERT_DIR if set (tilde-expanded), otherwise <home>/tls. Every
// per-file certificate setting (server/CA cert and key, and the certs
// subcommand's --out/--ca/--ca-key) derives its default from this
// directory unless the operator overrides it individually, mirroring the
// #165 DataDir/dedicated-path precedence: a specific setting always wins
// over the directory it would otherwise be derived from (#598).
func resolveCertDir() (string, error) {
	if v := os.Getenv("SEP2_CERT_DIR"); v != "" {
		expanded, err := config.ExpandHome(v)
		if err != nil {
			return "", fmt.Errorf("SEP2_CERT_DIR=%q: %w", v, err)
		}
		return expanded, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("SEP2_CERT_DIR is not set and the home directory could not be determined: set SEP2_CERT_DIR (or SEP2_CERT, SEP2_KEY, SEP2_CA, SEP2_CA_KEY) explicitly: %w", err)
	}
	return filepath.Join(home, "tls"), nil
}

// envPathOr resolves a filesystem-path setting: the env var if set, else
// fallback, with a leading tilde expanded per config.ExpandHome. Every
// SEP2_* setting that names a filesystem path goes through this rather
// than envOr, so an operator who writes ~/... into any of them gets the
// same expansion a shell would have given on a command line (#598).
func envPathOr(key, fallback string) (string, error) {
	expanded, err := config.ExpandHome(envOr(key, fallback))
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return expanded, nil
}

// expandCSVPaths splits a comma-separated list of filesystem paths and
// tilde-expands each entry independently, so SEP2_EXTRA_CLIENT_CAS gets
// the same treatment as every other path setting (#598).
func expandCSVPaths(csv string) ([]string, error) {
	entries := parseCSV(csv)
	if entries == nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		expanded, err := config.ExpandHome(e)
		if err != nil {
			return nil, fmt.Errorf("SEP2_EXTRA_CLIENT_CAS entry %q: %w", e, err)
		}
		out = append(out, expanded)
	}
	return out, nil
}

// resolveDir expands a leading tilde in an explicit --out-style flag
// value, or returns the default certificate directory when the flag was
// left at its empty default, so a certs subcommand run with no flags
// never falls back to a path inside the working directory (#598).
func resolveDir(flagValue string) (string, error) {
	if flagValue == "" {
		return resolveCertDir()
	}
	return config.ExpandHome(flagValue)
}

// resolveFile is resolveDir for a flag naming a single file (--ca,
// --ca-key): an explicit value is tilde-expanded as given; an empty one
// derives <default cert dir>/name.
func resolveFile(flagValue, name string) (string, error) {
	if flagValue == "" {
		dir, err := resolveCertDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, name), nil
	}
	return config.ExpandHome(flagValue)
}

// resolveOutAndCA resolves the --out, --ca and --ca-key flags shared by
// generate-server, generate-admin and generate-device: each is
// tilde-expanded if given explicitly, or derived from the default
// certificate directory if left empty (#598).
func resolveOutAndCA(outFlag, caFlag, caKeyFlag string) (out, ca, caKey string, err error) {
	out, err = resolveDir(outFlag)
	if err != nil {
		return "", "", "", err
	}
	ca, err = resolveFile(caFlag, "ca.crt")
	if err != nil {
		return "", "", "", err
	}
	caKey, err = resolveFile(caKeyFlag, "ca.key")
	if err != nil {
		return "", "", "", err
	}
	return out, ca, caKey, nil
}
