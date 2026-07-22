package hostnetwork

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// InitMeshNATChain ensures the WENDY-MESH chain exists in the nat table and
// that PREROUTING jumps into it. Idempotent, safe on every agent startup, and
// never flushes existing rules — the nat-table twin of InitMeshChain.
func InitMeshNATChain() error {
	if err := ensureNATChain(MeshChainName); err != nil {
		return fmt.Errorf("hostnetwork: ensure nat chain %s: %w", MeshChainName, err)
	}
	if err := ensurePreroutingJump(MeshChainName); err != nil {
		return fmt.Errorf("hostnetwork: ensure PREROUTING jump to %s: %w", MeshChainName, err)
	}
	return nil
}

func ensureNATChain(chain string) error {
	out, err := exec.Command("iptables", "-t", "nat", "-N", chain).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "already exists") {
		return nil
	}
	return fmt.Errorf("iptables -t nat -N %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
}

func ensurePreroutingJump(chain string) error {
	cmd := exec.Command("iptables", "-t", "nat", "-C", "PREROUTING", "-j", chain)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if exitCode(err) != 1 {
		return fmt.Errorf("iptables -t nat -C PREROUTING -j %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
	}
	out, err = exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-j", chain).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -A PREROUTING -j %s: %w (%s)", chain, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// meshRedirectArgs returns the nat rule (sans verb) steering one meshed
// container's TCP traffic toward the mesh service CIDR into the local mesh
// proxy port. Shared by add/remove/check so the three can never drift.
func meshRedirectArgs(containerIP, serviceCIDR string, proxyPort int) []string {
	return []string{
		"-t", "nat",
		"-s", containerIP + "/32",
		"-d", serviceCIDR,
		"-p", "tcp",
		"-j", "REDIRECT",
		"--to-ports", strconv.Itoa(proxyPort),
	}
}

// AddMeshRedirect idempotently installs the REDIRECT rule for one container.
func AddMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error {
	exists, err := meshRedirectExists(containerIP, serviceCIDR, proxyPort)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	args := append([]string{"-A", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -A %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RemoveMeshRedirect idempotently removes the REDIRECT rule for one container.
func RemoveMeshRedirect(containerIP, serviceCIDR string, proxyPort int) error {
	exists, err := meshRedirectExists(containerIP, serviceCIDR, proxyPort)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	args := append([]string{"-D", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables -t nat -D %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func meshRedirectExists(containerIP, serviceCIDR string, proxyPort int) (bool, error) {
	args := append([]string{"-C", MeshChainName}, meshRedirectArgs(containerIP, serviceCIDR, proxyPort)...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("iptables -t nat -C %s: %w (%s)", MeshChainName, err, strings.TrimSpace(string(out)))
}
