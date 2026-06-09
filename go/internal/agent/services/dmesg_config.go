package services

// DmesgDPIAConfirmFile must exist with non-empty content before kernel message
// collection starts. It serves as the filesystem-domain gate that prevents the
// env-var alone (WENDY_COLLECT_DMESG) from enabling collection.
//
// Defined here (no build tag) so callsites outside the linux-gated
// CollectDmesgLogs implementation can reference it for pre-checks.
const DmesgDPIAConfirmFile = "/etc/wendy/dmesg-dpia-confirmed"
