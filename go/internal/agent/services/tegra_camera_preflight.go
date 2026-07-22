package services

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"go.uber.org/zap"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const tegraFirmwareMismatchReason = "TEGRA_FIRMWARE_MISMATCH"

var (
	tegraReleaseFamilyRE = regexp.MustCompile(`(?i)\bR([0-9]{2})\b`)
	tegraBootFamilyRE    = regexp.MustCompile(`(?im)current\s+version\s*:\s*R?([0-9]{2})(?:\.|\b)`)
	tegraRevisionRE      = regexp.MustCompile(`(?i)REVISION\s*:\s*([0-9.]+)`)
)

type tegraVersions struct {
	RootfsFamily, BootFamily   string
	RootfsVersion, BootVersion string
}

func parseTegraVersions(release, slots string) (tegraVersions, bool) {
	r := tegraReleaseFamilyRE.FindStringSubmatch(release)
	b := tegraBootFamilyRE.FindStringSubmatch(slots)
	if len(r) != 2 || len(b) != 2 {
		return tegraVersions{}, false
	}
	v := tegraVersions{RootfsFamily: r[1], BootFamily: b[1], RootfsVersion: "R" + r[1], BootVersion: "R" + b[1]}
	if rev := tegraRevisionRE.FindStringSubmatch(release); len(rev) == 2 {
		v.RootfsVersion += "." + strings.Trim(rev[1], ".")
	}
	if full := regexp.MustCompile(`(?im)current\s+version\s*:\s*(R?[0-9]+(?:\.[0-9]+)*)`).FindStringSubmatch(slots); len(full) == 2 {
		v.BootVersion = strings.ToUpper(full[1])
		if !strings.HasPrefix(v.BootVersion, "R") {
			v.BootVersion = "R" + v.BootVersion
		}
	}
	return v, true
}

func (s *VideoService) preflightTegraCSI(ctx context.Context) error {
	release, releaseErr := s.readTegraRelease()
	slots, slotsErr := s.dumpBootSlots(ctx)
	if releaseErr != nil || slotsErr != nil {
		s.logger.Warn("could not determine Jetson camera firmware compatibility; allowing CSI stream",
			zap.Error(fmt.Errorf("nv_tegra_release: %v; nvbootctrl: %v", releaseErr, slotsErr)))
		return nil
	}
	versions, ok := parseTegraVersions(string(release), string(slots))
	if !ok {
		s.logger.Warn("could not parse Jetson firmware versions; allowing CSI stream")
		return nil
	}
	if versions.RootfsFamily == versions.BootFamily {
		return nil
	}
	st := status.New(codes.FailedPrecondition, fmt.Sprintf(
		"Jetson rootfs %s does not match boot firmware %s; CSI camera drivers cannot be initialized safely",
		versions.RootfsVersion, versions.BootVersion))
	withInfo, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason: tegraFirmwareMismatchReason,
		Domain: "wendy.dev",
		Metadata: map[string]string{
			"rootfs_l4t":        versions.RootfsVersion,
			"boot_firmware_l4t": versions.BootVersion,
		},
	})
	if err != nil {
		return st.Err()
	}
	return withInfo.Err()
}
