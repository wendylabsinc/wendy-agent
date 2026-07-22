//go:build windows

package winusb

// Build and Authenticode-sign a driver catalog (.cat) for the generated INF,
// entirely via inbox wintrust/crypt32/mssign32 APIs (no makecat/signtool). A
// catalog is a signed manifest of file hashes; Windows validates a driver
// package by hashing its .inf and finding that hash as a signed catalog member.
//
// The member's indirect data (the ASN.1 the catalog actually stores) is produced
// by the file's Subject Interface Package via CryptSIPCreateIndirectData — the
// same path makecat uses — so we don't hand-encode ASN.1.

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// buildAndSignCatalog creates catPath as a SHA-256 catalog containing memberFile
// (the INF), tagged for the given hardware IDs and modern Windows, then signs it
// with cert. memberFile must live in the same directory the catalog will be
// staged from (catalogs reference members by base name).
func buildAndSignCatalog(catPath, memberFile string, hwids []string, cert *signingCert) error {
	if err := buildCatalog(catPath, memberFile, hwids); err != nil {
		return err
	}
	if err := signCatalog(catPath, cert); err != nil {
		return fmt.Errorf("signing catalog: %w", err)
	}
	return nil
}

// buildCatalog creates a SHA-256 catalog at catPath with one member (the INF).
func buildCatalog(catPath, memberFile string, hwids []string) error {
	wcat, err := windows.UTF16PtrFromString(catPath)
	if err != nil {
		return err
	}
	// Create a new SHA-256 (version 2) catalog. hProv=0 uses the default.
	hCat, _, e := procCryptCATOpen.Call(
		uintptr(unsafe.Pointer(wcat)),
		uintptr(cryptcatOpenCreateNew),
		0,
		uintptr(0x200), // CRYPTCAT_VERSION_2 (SHA-256)
		uintptr(x509ASNEncoding|pkcs7ASNEncoding),
	)
	if hCat == 0 || hCat == invalidHandle {
		return fmt.Errorf("CryptCATOpen: %w", e)
	}
	defer procCryptCATClose.Call(hCat)

	base := baseName(memberFile)
	wbase, _ := windows.UTF16PtrFromString(base)

	// Replicate the structure the inbox New-FileCatalog (-CatalogVersion 2)
	// produces, which Windows Code Integrity accepts for driver-package
	// verification: two members per file — one keyed by the file's SHA-1 (CI's
	// legacy INF lookup) and one by its SHA-256 (carrying the SPC indirect data).
	// Each member carries a "FilePath" name attribute. No OSAttr/OS/HWID.

	// SHA-1 member: tag = uppercase-hex SHA-1, with SHA-1 SPC indirect data (CI's
	// legacy INF lookup keys on the SHA-1 tag).
	sha1Hex, _, err := hashFileHex(memberFile, "SHA1")
	if err != nil {
		return err
	}
	sha1Indirect, err := sipIndirectDataForFile(memberFile, oidSHA1)
	if err != nil {
		return err
	}
	if err := addMember(hCat, wbase, base, sha1Hex, sha1Indirect); err != nil {
		return fmt.Errorf("SHA-1 member: %w", err)
	}

	// SHA-256 member: tag = uppercase-hex SHA-256, with SHA-256 SPC indirect data.
	sha256Hex, _, err := hashFileHex(memberFile, "SHA256")
	if err != nil {
		return err
	}
	sha256Indirect, err := sipIndirectDataForFile(memberFile, oidSHA256)
	if err != nil {
		return err
	}
	if err := addMember(hCat, wbase, base, sha256Hex, sha256Indirect); err != nil {
		return fmt.Errorf("SHA-256 member: %w", err)
	}

	if r, _, e := procCryptCATPersistStore.Call(hCat); r == 0 {
		return fmt.Errorf("CryptCATPersistStore: %w", e)
	}
	return nil
}

// addMember adds one catalog member (tag = hex hash) with a "FilePath" name
// attribute. indirect may be nil (no SPC indirect data, as on the SHA-1 member).
func addMember(hCat uintptr, wbase *uint16, base, tag string, indirect []byte) error {
	wtag, _ := windows.UTF16PtrFromString(tag)
	subjType := sipFlatImageGUID
	var indPtr uintptr
	if len(indirect) > 0 {
		indPtr = uintptr(unsafe.Pointer(&indirect[0]))
	}
	member, _, e := procCryptCATPutMemberInfo.Call(
		hCat,
		uintptr(unsafe.Pointer(wbase)),
		uintptr(unsafe.Pointer(wtag)),
		uintptr(unsafe.Pointer(&subjType)),
		uintptr(0x200),
		uintptr(len(indirect)),
		indPtr,
	)
	if member == 0 {
		return fmt.Errorf("CryptCATPutMemberInfo: %w", e)
	}
	if err := putMemberAttr(hCat, member, "FilePath", base); err != nil {
		return err
	}
	return nil
}

// putMemberAttr adds an authenticated ASCII name/value attribute to a member.
func putMemberAttr(hCat, member uintptr, name, value string) error {
	wname, _ := windows.UTF16PtrFromString(name)
	wval, _ := windows.UTF16FromString(value)
	valBytes := uint32(len(wval) * 2) // includes terminating NUL, as wide bytes
	r, _, e := procCryptCATPutAttrInfo.Call(
		hCat,
		member,
		uintptr(unsafe.Pointer(wname)),
		uintptr(cryptcatAttrAuthenticated|cryptcatAttrNameASCII|cryptcatAttrDataASCII),
		uintptr(valBytes),
		uintptr(unsafe.Pointer(&wval[0])),
	)
	if r == 0 {
		return fmt.Errorf("CryptCATPutAttrInfo(%s): %w", name, e)
	}
	return nil
}

// putCatAttr adds an authenticated ASCII catalog-level attribute.
func putCatAttr(hCat uintptr, name, value string) error {
	wname, _ := windows.UTF16PtrFromString(name)
	wval, _ := windows.UTF16FromString(value)
	valBytes := uint32(len(wval) * 2)
	r, _, e := procCryptCATPutCatAttrInfo.Call(
		hCat,
		uintptr(unsafe.Pointer(wname)),
		uintptr(cryptcatAttrAuthenticated|cryptcatAttrNameASCII|cryptcatAttrDataASCII),
		uintptr(valBytes),
		uintptr(unsafe.Pointer(&wval[0])),
	)
	if r == 0 {
		return fmt.Errorf("CryptCATPutCatAttrInfo(%s): %w", name, e)
	}
	return nil
}

// hashFileHex returns the uppercase-hex hash of path under the given algorithm
// ("SHA1"/"SHA256"), plus the raw bytes, computed via the catalog admin SHA path.
func hashFileHex(path, algo string) (string, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	hash, err := calcFileHash(windows.Handle(f.Fd()), algo)
	if err != nil {
		return "", nil, err
	}
	return strings.ToUpper(hexEncode(hash)), hash, nil
}

// sipIndirectDataForFile returns the encoded SIP_INDIRECT_DATA buffer for a file
// (as CryptCATPutMemberInfo expects) using the flat-file SIP with the given
// digest OID (oidSHA1 or oidSHA256).
func sipIndirectDataForFile(path, digestOIDStr string) (indirect []byte, err error) {
	wpath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	hFile := windows.Handle(f.Fd())

	// Use the flat-file SIP directly. CryptSIPRetrieveSubjectGuid only recognizes
	// PE/CAB/CAT/CTL subjects and fails (TRUST_E_SUBJECT_FORM_UNKNOWN) on a plain
	// .inf, so we name the flat-file SIP by its well-known GUID
	// (CRYPT_SUBJTYPE_FLAT_IMAGE) — the same subject type makecat uses for .inf.
	guid := sipFlatImageGUID

	var dispatch sipDispatchInfo
	dispatch.cbSize = uint32(unsafe.Sizeof(dispatch))
	if r, _, e := procCryptSIPLoad.Call(
		uintptr(unsafe.Pointer(&guid)),
		0,
		uintptr(unsafe.Pointer(&dispatch)),
	); r == 0 {
		return nil, fmt.Errorf("CryptSIPLoad: %w", e)
	}
	if dispatch.pfCreate == 0 {
		return nil, fmt.Errorf("SIP has no CreateIndirectData")
	}

	digestOID, _ := windows.BytePtrFromString(digestOIDStr)
	subj := sipSubjectInfo{
		cbSize:          uint32(unsafe.Sizeof(sipSubjectInfo{})),
		pgSubjectType:   &guid,
		hFile:           hFile,
		pwsFileName:     wpath,
		dwEncodingType:  x509ASNEncoding | pkcs7ASNEncoding,
		DigestAlgorithm: cryptAlgorithmIdentifier{pszObjId: digestOID},
	}

	// Size, allocate, then fill.
	var cb uint32
	r, _, e := syscallN(dispatch.pfCreate,
		uintptr(unsafe.Pointer(&subj)),
		uintptr(unsafe.Pointer(&cb)),
		0,
	)
	if r == 0 || cb == 0 {
		return nil, fmt.Errorf("CryptSIPCreateIndirectData (size): %w", e)
	}
	buf := make([]byte, cb)
	r, _, e = syscallN(dispatch.pfCreate,
		uintptr(unsafe.Pointer(&subj)),
		uintptr(unsafe.Pointer(&cb)),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if r == 0 {
		return nil, fmt.Errorf("CryptSIPCreateIndirectData: %w", e)
	}
	return buf, nil
}

// calcFileHash hashes an open file under the given algorithm ("SHA1"/"SHA256")
// via the catalog admin hashing path.
func calcFileHash(hFile windows.Handle, algorithm string) ([]byte, error) {
	algo, _ := windows.UTF16PtrFromString(algorithm)
	var hCatAdmin uintptr
	if r, _, e := procCryptCATAdminAcquireContext2.Call(
		uintptr(unsafe.Pointer(&hCatAdmin)),
		0,
		uintptr(unsafe.Pointer(algo)),
		0,
		0,
	); r == 0 {
		return nil, fmt.Errorf("CryptCATAdminAcquireContext2: %w", e)
	}
	defer procCryptCATAdminReleaseContext.Call(hCatAdmin, 0)

	var cbHash uint32
	// Size the hash.
	procCryptCATAdminCalcHashFromFileHandle2.Call(
		hCatAdmin,
		uintptr(hFile),
		uintptr(unsafe.Pointer(&cbHash)),
		0,
		0,
	)
	if cbHash == 0 {
		return nil, fmt.Errorf("CryptCATAdminCalcHashFromFileHandle2 returned zero hash size")
	}
	hash := make([]byte, cbHash)
	if r, _, e := procCryptCATAdminCalcHashFromFileHandle2.Call(
		hCatAdmin,
		uintptr(hFile),
		uintptr(unsafe.Pointer(&cbHash)),
		uintptr(unsafe.Pointer(&hash[0])),
		0,
	); r == 0 {
		return nil, fmt.Errorf("CryptCATAdminCalcHashFromFileHandle2: %w", e)
	}
	return hash, nil
}

// signCatalog Authenticode-signs catPath with cert (SHA-256), pulling the private
// key via the cert's linked key container.
func signCatalog(catPath string, cert *signingCert) error {
	wcat, err := windows.UTF16PtrFromString(catPath)
	if err != nil {
		return err
	}
	fileInfo := signerFileInfo{
		cbSize:       uint32(unsafe.Sizeof(signerFileInfo{})),
		pwszFileName: wcat,
	}
	var idx uint32
	subject := signerSubjectInfo{
		cbSize:          uint32(unsafe.Sizeof(signerSubjectInfo{})),
		pdwIndex:        &idx,
		dwSubjectChoice: signerSubjectFile,
		pSignerFileInfo: &fileInfo,
	}
	storeInfo := signerCertStoreInfo{
		cbSize:       uint32(unsafe.Sizeof(signerCertStoreInfo{})),
		pSigningCert: cert.ctx,
		dwCertPolicy: signerCertPolicyChain,
	}
	signerCertS := signerCert{
		cbSize:         uint32(unsafe.Sizeof(signerCert{})),
		dwCertChoice:   signerCertStore,
		pCertStoreInfo: &storeInfo,
	}
	sigInfo := signerSignatureInfo{
		cbSize:       uint32(unsafe.Sizeof(signerSignatureInfo{})),
		algidHash:    calgSHA256,
		dwAttrChoice: signerNoAttr,
	}
	var ctx uintptr
	r, _, e := procSignerSignEx.Call(
		0,
		uintptr(unsafe.Pointer(&subject)),
		uintptr(unsafe.Pointer(&signerCertS)),
		uintptr(unsafe.Pointer(&sigInfo)),
		0, // pProviderInfo
		0, // pwszHttpTimeStamp
		0, // psRequest
		0, // pSipData
		uintptr(unsafe.Pointer(&ctx)),
	)
	// SignerSignEx returns an HRESULT; S_OK == 0.
	if int32(r) != 0 {
		return fmt.Errorf("SignerSignEx: HRESULT 0x%08x (%v)", uint32(r), e)
	}
	if ctx != 0 {
		procSignerFreeSignerContext.Call(ctx)
	}
	return nil
}

const invalidHandle = ^uintptr(0)

// sipFlatImageGUID is CRYPT_SUBJTYPE_FLAT_IMAGE — the Subject Interface Package
// for arbitrary flat files (used to hash .inf into the catalog).
var sipFlatImageGUID = windows.GUID{
	Data1: 0xDE351A42,
	Data2: 0x8E59,
	Data3: 0x11D0,
	Data4: [8]byte{0x8C, 0x47, 0x00, 0xC0, 0x4F, 0xC2, 0x95, 0xEE},
}

// syscallN invokes a raw function pointer (e.g. a SIP dispatch entry) with args.
func syscallN(fn uintptr, a ...uintptr) (r1, r2 uintptr, err error) {
	return syscall.SyscallN(fn, a...)
}

// baseName returns the last path element (catalog members are keyed by base name).
func baseName(p string) string {
	p = strings.ReplaceAll(p, "/", "\\")
	if i := strings.LastIndex(p, "\\"); i >= 0 {
		return p[i+1:]
	}
	return p
}

const hexDigits = "0123456789abcdef"

func hexEncode(b []byte) string {
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexDigits[v>>4]
		out[i*2+1] = hexDigits[v&0xf]
	}
	return string(out)
}
