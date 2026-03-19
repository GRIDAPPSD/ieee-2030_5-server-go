package main

import (
	"encoding/asn1"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
)

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
	outDir := fs.String("out", "./certs", "Output directory")
	years := fs.Int("years", 10, "Validity in years (0 = indefinite)")
	fs.Parse(args)

	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: *org,
		CommonName:   *cn,
		ValidYears:   *years,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0700); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(*outDir, "ca.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "ca.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("CA certificate: %s/ca.crt\n", *outDir)
	fmt.Printf("CA private key: %s/ca.key\n", *outDir)
	return nil
}

func runGenerateServer(args []string) error {
	fs := flag.NewFlagSet("generate-server", flag.ExitOnError)
	caFile := fs.String("ca", "./certs/ca.crt", "CA certificate PEM")
	caKeyFile := fs.String("ca-key", "./certs/ca.key", "CA private key PEM")
	hosts := fs.String("hosts", "localhost,127.0.0.1", "Comma-separated hosts (DNS/IP)")
	cn := fs.String("cn", "IEEE 2030.5 Server", "Common name")
	outDir := fs.String("out", "./certs", "Output directory")
	years := fs.Int("years", 1, "Validity in years")
	fs.Parse(args)

	caCert, caKey, err := certs.LoadCA(*caFile, *caKeyFile)
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

	if err := os.WriteFile(filepath.Join(*outDir, "server.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "server.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("Server certificate: %s/server.crt\n", *outDir)
	fmt.Printf("Server private key: %s/server.key\n", *outDir)
	return nil
}

func runGenerateAdmin(args []string) error {
	fs := flag.NewFlagSet("generate-admin", flag.ExitOnError)
	caFile := fs.String("ca", "./certs/ca.crt", "CA certificate PEM")
	caKeyFile := fs.String("ca-key", "./certs/ca.key", "CA private key PEM")
	cn := fs.String("cn", "IEEE 2030.5 Admin", "Common name")
	outDir := fs.String("out", "./certs", "Output directory")
	years := fs.Int("years", 1, "Validity in years")
	fs.Parse(args)

	caCert, caKey, err := certs.LoadCA(*caFile, *caKeyFile)
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

	if err := os.WriteFile(filepath.Join(*outDir, "admin.crt"), certPEM, 0644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "admin.key"), keyPEM, 0600); err != nil {
		return err
	}

	fmt.Printf("Admin certificate: %s/admin.crt\n", *outDir)
	fmt.Printf("Admin private key: %s/admin.key\n", *outDir)
	return nil
}

func runGenerateDevice(args []string) error {
	fs := flag.NewFlagSet("generate-device", flag.ExitOnError)
	caFile := fs.String("ca", "./certs/ca.crt", "CA certificate PEM")
	caKeyFile := fs.String("ca-key", "./certs/ca.key", "CA private key PEM")
	deviceType := fs.Int("device-type", 1, "Device type (1=generic, 2=mobile, 3=postMfg)")
	hwSerial := fs.String("hw-serial", "", "Hardware serial number")
	name := fs.String("name", "device", "Output filename prefix")
	outDir := fs.String("out", "./certs", "Output directory")
	isTest := fs.Bool("test", false, "Generate test certificate")
	fs.Parse(args)

	caCert, caKey, err := certs.LoadCA(*caFile, *caKeyFile)
	if err != nil {
		return err
	}

	certPEM, keyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceType(*deviceType),
		HWType:      asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 99},
		HWSerialNum: *hwSerial,
		IsTestCert:  *isTest,
	})
	if err != nil {
		return err
	}

	certFile := filepath.Join(*outDir, *name+".crt")
	keyFile := filepath.Join(*outDir, *name+".key")

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

// suppress unused import warning
var _ = strconv.Itoa
