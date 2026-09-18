package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// validDeviceNamePattern is the strict allowlist for the --name flag on
// generate-device. The result is joined into an output filesystem path
// (filepath.Join does not reject `..`), so we constrain the input to
// ASCII alnum + `.`, `-`, `_` with no leading dot. Defense-in-depth:
// callers (the make new-device script) also validate, but anyone calling
// the binary directly is protected here too.
var validDeviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

func runCerts(args []string) error {
	if len(args) < 1 {
		printCertsUsage()
		return fmt.Errorf("subcommand required")
	}

	switch args[0] {
	case "generate-ca":
		return runGenerateCA(args[1:])
	case "generate-server":
		return runGenerateServer(args[1:])
	case "generate-admin":
		return runGenerateAdmin(args[1:])
	case "generate-device":
		return runGenerateDevice(args[1:])
	default:
		printCertsUsage()
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

func runGenerateCA(args []string) error {
	fs := flag.NewFlagSet("generate-ca", flag.ExitOnError)
	org := fs.String("org", "IEEE 2030.5", "Organization name")
	cn := fs.String("cn", "IEEE 2030.5 Root CA", "Common name")
	outDir := fs.String("out", "", "Output directory (default: SEP2_CERT_DIR, or ~/tls)")
	years := fs.Int("years", 10, "Validity in years (0 = indefinite)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedOut, err := resolveDir(*outDir)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: *org,
		CommonName:   *cn,
		ValidYears:   *years,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(resolvedOut, 0700); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(resolvedOut, "ca.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resolvedOut, "ca.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("CA certificate: %s/ca.crt\n", resolvedOut)
	fmt.Printf("CA private key: %s/ca.key\n", resolvedOut)
	return nil
}

func runGenerateServer(args []string) error {
	fs := flag.NewFlagSet("generate-server", flag.ExitOnError)
	caFile := fs.String("ca", "", "CA certificate PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.crt)")
	caKeyFile := fs.String("ca-key", "", "CA private key PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.key)")
	hosts := fs.String("hosts", "localhost,127.0.0.1", "Comma-separated hosts (DNS/IP)")
	cn := fs.String("cn", "IEEE 2030.5 Server", "Common name")
	outDir := fs.String("out", "", "Output directory (default: SEP2_CERT_DIR, or ~/tls)")
	years := fs.Int("years", 1, "Validity in years")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedOut, resolvedCA, resolvedCAKey, err := resolveOutAndCA(*outDir, *caFile, *caKeyFile)
	if err != nil {
		return err
	}

	caCert, caKey, err := certs.LoadCA(resolvedCA, resolvedCAKey)
	if err != nil {
		return err
	}

	var hostList []string
	for _, h := range splitCSV(*hosts) {
		if h != "" {
			hostList = append(hostList, h)
		}
	}

	certPEM, keyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      hostList,
		CommonName: *cn,
		ValidYears: *years,
	})
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(resolvedOut, "server.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resolvedOut, "server.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("Server certificate: %s/server.crt\n", resolvedOut)
	fmt.Printf("Server private key: %s/server.key\n", resolvedOut)
	return nil
}

func runGenerateAdmin(args []string) error {
	fs := flag.NewFlagSet("generate-admin", flag.ExitOnError)
	caFile := fs.String("ca", "", "CA certificate PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.crt)")
	caKeyFile := fs.String("ca-key", "", "CA private key PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.key)")
	cn := fs.String("cn", "IEEE 2030.5 Admin", "Common name")
	outDir := fs.String("out", "", "Output directory (default: SEP2_CERT_DIR, or ~/tls)")
	years := fs.Int("years", 1, "Validity in years")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedOut, resolvedCA, resolvedCAKey, err := resolveOutAndCA(*outDir, *caFile, *caKeyFile)
	if err != nil {
		return err
	}

	caCert, caKey, err := certs.LoadCA(resolvedCA, resolvedCAKey)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: *cn,
		ValidYears: *years,
	})
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(resolvedOut, "admin.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(resolvedOut, "admin.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("Admin certificate: %s/admin.crt\n", resolvedOut)
	fmt.Printf("Admin private key: %s/admin.key\n", resolvedOut)
	return nil
}

func runGenerateDevice(args []string) error {
	fs := flag.NewFlagSet("generate-device", flag.ContinueOnError)
	caFile := fs.String("ca", "", "CA certificate PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.crt)")
	caKeyFile := fs.String("ca-key", "", "CA private key PEM (default: SEP2_CERT_DIR, or ~/tls, plus ca.key)")
	deviceType := fs.Int("device-type", 1, "Device type (1=generic, 2=mobile, 3=postMfg)")
	hwSerial := fs.String("hw-serial", "", "Hardware serial number (required for CSIP section 6.2 HardwareModuleName SAN)")
	hwType := fs.String("hw-type", "", "manufacturer PEN OID (e.g. 1.3.6.1.4.1.<PEN>) - required for CSIP HardwareModuleName SAN")
	name := fs.String("name", "device", "Output filename prefix")
	outDir := fs.String("out", "", "Output directory (default: SEP2_CERT_DIR, or ~/tls)")
	isTest := fs.Bool("test", false, "Generate test certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *hwSerial == "" {
		return fmt.Errorf("-hw-serial is required (CSIP section 6.2 HardwareModuleName SAN)")
	}
	if *hwType == "" {
		return fmt.Errorf("-hw-type is required: pass your manufacturer's PEN OID (e.g. 1.3.6.1.4.1.<PEN>)")
	}
	if !validDeviceNamePattern.MatchString(*name) {
		return fmt.Errorf("-name must match %s (got %q): allowed characters are ASCII letters, digits, '.', '-', '_'; no leading dot, no path separators, max 64 chars", validDeviceNamePattern, *name)
	}
	hwTypeOID, err := certs.ParseOID(*hwType)
	if err != nil {
		return fmt.Errorf("-hw-type: %w", err)
	}

	resolvedOut, resolvedCA, resolvedCAKey, err := resolveOutAndCA(*outDir, *caFile, *caKeyFile)
	if err != nil {
		return err
	}

	caCert, caKey, err := certs.LoadCA(resolvedCA, resolvedCAKey)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceType(*deviceType),
		HWType:      hwTypeOID,
		HWSerialNum: *hwSerial,
		IsTestCert:  *isTest,
	})
	if err != nil {
		return err
	}

	certFile := filepath.Join(resolvedOut, *name+".crt")
	keyFile := filepath.Join(resolvedOut, *name+".key")

	if err := os.WriteFile(certFile, certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		return err
	}

	// Compute and display SFDI/LFDI
	cert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		return err
	}

	sfdi := sepTLS.SFDI(cert)
	lfdi := sepTLS.LFDI(cert)

	fmt.Printf("Device certificate: %s\n", certFile)
	fmt.Printf("Device private key: %s\n", keyFile)
	fmt.Printf("SFDI: %s (%s)\n", sfdi, sepTLS.FormatSFDI(sfdi))
	fmt.Printf("LFDI: %s\n", lfdi)
	return nil
}

func splitCSV(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func printCertsUsage() {
	fmt.Fprintln(os.Stderr, "Usage: sep2server certs <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  generate-ca      Generate root CA certificate")
	fmt.Fprintln(os.Stderr, "  generate-server  Generate server certificate")
	fmt.Fprintln(os.Stderr, "  generate-admin   Generate admin certificate")
	fmt.Fprintln(os.Stderr, "  generate-device  Generate device certificate")
}
