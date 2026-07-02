package rcm

// USB identifiers for Jetsons in recovery mode (vendor 0x0955, PID 0x70<chip-id>).
const (
	VendorNVIDIA = 0x0955
	ProductOrin  = 0x7023 // T234 (AGX Orin, "APX"); confirmed on live hardware
	ProductThor  = 0x7026 // T264 (AGX Thor); confirmed on live hardware
)
