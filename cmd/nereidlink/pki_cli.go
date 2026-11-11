// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// pki_cli.go exposes the device mTLS private PKI: "pki init" creates the
// CA and MQTT server certificate, "pki device" signs a certificate for
// one X509 credential. Certificates never touch the database; the
// operator registers the issued certificate PEM on the device credential.

package main

import (
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nereidai/nereidlink/internal/pki"
)

// pkiCommand groups the private PKI management commands.
func pkiCommand() *cobra.Command {
	group := &cobra.Command{
		Use:   "pki",
		Short: "Manage the device mTLS private PKI",
	}
	group.AddCommand(pkiInitCommand(), pkiEnsureCommand(), pkiServerCommand(), pkiDeviceCommand())
	return group
}

// pkiEnsureCommand returns the idempotent variant of "pki init" intended for
// container init steps: it creates the CA/server cert only when absent and is
// a no-op on an already-provisioned PKI volume.
func pkiEnsureCommand() *cobra.Command {
	var dir string
	var hosts []string
	command := &cobra.Command{
		Use:   "ensure",
		Short: "Create the CA and server cert if missing (idempotent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return pki.EnsureCA(dir, hosts)
		},
	}
	command.Flags().StringVar(&dir, "dir", "configs/pki", "output directory")
	command.Flags().StringSliceVar(&hosts, "hosts",
		[]string{"localhost", "127.0.0.1"}, "server certificate host names and IPs")
	return command
}

// pkiInitCommand returns the command that creates a CA plus server cert.
func pkiInitCommand() *cobra.Command {
	var dir string
	var hosts []string
	command := &cobra.Command{
		Use:   "init",
		Short: "Create a CA and the MQTT server certificate",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pki.InitCA(dir, hosts); err != nil {
				return err
			}
			// Name verification failures are the most common follow-up
			// problem, so surface the addresses worth adding up front.
			if local := localAddresses(); len(local) > 0 {
				cmd.Printf("server certificate covers: %s\n", strings.Join(hosts, ", "))
				cmd.Printf("add a reachable address with: pki server --hosts %s\n",
					strings.Join(local, ","))
			}
			return nil
		},
	}
	command.Flags().StringVar(&dir, "dir", "configs/pki", "output directory")
	command.Flags().StringSliceVar(&hosts, "hosts",
		[]string{"localhost", "127.0.0.1"}, "server certificate host names and IPs")
	return command
}

// pkiServerCommand returns the command that re-signs the MQTT server
// certificate alone, so adding a hostname or LAN address does not require
// rotating the CA and invalidating every issued device certificate.
func pkiServerCommand() *cobra.Command {
	var dir string
	var hosts []string
	command := &cobra.Command{
		Use:   "server",
		Short: "Re-sign the MQTT server certificate with new hosts",
		Long: "Re-signs configs/pki/server.pem from the existing CA so clients " +
			"connecting by another name or address pass hostname verification. " +
			"Already issued device certificates stay valid.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pki.IssueServer(dir, hosts); err != nil {
				return err
			}
			cmd.Printf("server certificate re-signed for: %s\n", strings.Join(hosts, ", "))
			cmd.Println("restart the service so the new certificate is served")
			return nil
		},
	}
	command.Flags().StringVar(&dir, "dir", "configs/pki", "PKI directory holding the CA")
	command.Flags().StringSliceVar(&hosts, "hosts",
		[]string{"localhost", "127.0.0.1"}, "server certificate host names and IPs")
	return command
}

// localAddresses lists the non-loopback unicast IPv4 addresses of this
// host. They are the addresses a remote MQTT client is most likely to
// dial, and therefore the ones missing from a certificate that only
// covers localhost.
func localAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var found []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ipNet, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipNet.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
				found = append(found, ip4.String())
			}
		}
	}
	return found
}

// pkiDeviceCommand returns the command issuing a device client certificate.
func pkiDeviceCommand() *cobra.Command {
	var caDir string
	var cn string
	var outDir string
	var validity time.Duration
	command := &cobra.Command{
		Use:   "device",
		Short: "Sign a client certificate for one X509 credential",
		RunE: func(cmd *cobra.Command, args []string) error {
			keyPath := outDir + "/" + cn + "-key.pem"
			certPath := outDir + "/" + cn + ".pem"
			if err := pki.IssueDevice(caDir, cn, keyPath, certPath, validity); err != nil {
				return err
			}
			cmd.Println("certificate:", certPath)
			cmd.Println("private key:", keyPath)
			return nil
		},
	}
	command.Flags().StringVar(&caDir, "ca-dir", "configs/pki", "CA directory")
	command.Flags().StringVar(&cn, "cn", "", "common name, normally the credentials id")
	command.Flags().StringVar(&outDir, "out", "configs/pki/devices", "output directory")
	command.Flags().DurationVar(&validity, "validity", 0, "certificate validity (default 1 year)")
	_ = command.MarkFlagRequired("cn")
	return command
}
