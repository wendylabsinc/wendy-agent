package rcm

// USB identifiers for Jetsons in recovery mode (vendor 0x0955). T234 modules
// enumerate as PID 0x7<module>23 — the low byte is the chip ID and the second
// nibble the module SKU — so every module in the Orin family has its own
// recovery PID and all of them must be matched.
const (
	VendorNVIDIA = 0x0955

	ProductOrinAGX32 = 0x7023 // T234 AGX Orin 32GB ("APX"); confirmed on live hardware
	ProductOrinAGX64 = 0x7223 // T234 AGX Orin 64GB
	ProductOrinNX16  = 0x7323 // T234 Orin NX 16GB
	ProductOrinNX8   = 0x7423 // T234 Orin NX 8GB
	ProductOrinNano8 = 0x7523 // T234 Orin Nano 8GB (devkit module); confirmed on live hardware
	ProductOrinNano4 = 0x7623 // T234 Orin Nano 4GB

	ProductThor = 0x7026 // T264 (AGX Thor); confirmed on live hardware
)

// t234Modules maps each T234 recovery PID to its module name.
var t234Modules = map[uint16]string{
	ProductOrinAGX32: "AGX Orin 32GB",
	ProductOrinAGX64: "AGX Orin 64GB",
	ProductOrinNX16:  "Orin NX 16GB",
	ProductOrinNX8:   "Orin NX 8GB",
	ProductOrinNano8: "Orin Nano 8GB",
	ProductOrinNano4: "Orin Nano 4GB",
}

// IsT234RecoveryPID reports whether pid is a T234 (Orin-family) recovery PID.
func IsT234RecoveryPID(pid uint16) bool {
	_, ok := t234Modules[pid]
	return ok
}

// T234ModuleName names the Orin-family module behind a recovery PID.
func T234ModuleName(pid uint16) (string, bool) {
	name, ok := t234Modules[pid]
	return name, ok
}
