# `tegrasign_v3` signing for T264 (Thor), ODM-open / non-secure / zerosbk

This document describes exactly what NVIDIA's `tegrasign_v3.py` does in the **ODM-open, non-secure** boot mode, where no real key is provisioned: zero Secure Boot Key (SBK), no Public Key Cryptography (PKC) / Rivest-Shamir-Adleman (RSA), no Elliptic Curve Cryptography (ECC), no Edwards-curve Digital Signature Algorithm (EdDSA). The goal is a faithful reimplementation in Go.

All line references are to the extracted sources under `/tmp/t264re/`. The two callers that matter are `tegraflash_impl_t264.py` (production tegraflash) and `bootburn_t264_py/bootburn_thor.py` (Bootburn). They are functionally identical for the parts documented here.

The headline result: **in ODM-open / zerosbk mode the cryptographic signature is a no-op (16 zero bytes), and the only thing that actually protects the image is a SHA digest that `tegrahost_v2` writes into the Boot Component Header (BCH).** `tegrasign_v3` itself produces the zero "signature"; `tegrahost_v2 --updatesigheader ... zerosbk` is what writes the real integrity hash.

---

## 1. How "zerosbk" mode is selected

The mode is not chosen by `tegrasign_v3` directly; the caller decides. In `tegraflash_impl_t264.py` the default is set in the class body:

```python
tegrasign_values = {
    '--mode': 'zerosbk',
    '--pubkeyhash': 'pub_key.key',
}
```

`tegraflash_get_key_mode()` (line 477) overrides this by asking tegrasign for the mode of `--key`:

```python
def tegraflash_get_key_mode(self):
    self.call_tegrasign(None, 'mode.txt', None,
                        values['--key'], None, None, None, None, None, None)
    with open('mode.txt') as mode_file:
        self.tegrasign_values['--mode'] = mode_file.read()
```

When `--key` is `None` (the ODM-open case), `extract_key()` in `tegrasign_v3.py` (line 117) takes the `keyfilename == 'None'` branch:

```python
if keyfilename == 'None':
    ...
    p_key.mode = NvTegraSign_SBK          # = 'SBK'
    p_key.key.aeskey = bytearray(16)      # 16 zero bytes
    info_print('Assuming zero filled SBK key')
```

So `p_key.mode == 'SBK'` and the AES key is sixteen zero bytes. `get_mode_str()` (util line 919) then distinguishes this from a real SBK:

```python
else:
    if (is_modetxt and is_zero_aes(pKey)):
        mode_str = 'zerosbk'
    else:
        mode_str = 'sbk'
```

`is_zero_aes()` (util line 979) simply returns true when every byte of the AES key is zero. Hence `mode.txt` reads `zerosbk` and that string flows back into `tegrasign_values['--mode']`. Note that in the per-file signing XML the tag is always `sbk` (the comment at util line 916 spells this out: "for xml tag, zerosbk and sbk both returns 'sbk'").

---

## 2. Signing entry points used by the T264 flow

There are two image-signing helpers, both in `tegraflash_impl_t264.py`:

- `tegraflash_oem_sign_file(in_file, magic_id, partition_type)` (line 2412) — sign only (no encryption). This is the ODM-open path; called for every image whose partition has `oem_sign='true'` and for one-off binaries like the Platform Configuration Table (PCT), Memory Boot Configuration Table (MB2-BCT), MB2, and so on.
- `tegraflash_oem_enc_and_sign_file(in_file, magic_id, ...)` (line 2539) — encrypt then sign. Only reached when `--encrypt_key` is supplied, i.e. **not** the ODM-open case. Its zerosbk tail (lines 2647-2672) is structurally identical to the sign-only path.

Bulk image signing for partitions goes through `tegraflash_sign_images()` (line 630) → `tegraflash_generate_signing_list()` (line 595, runs `tegrahost_v2 --partitionlayout ... --list images_list.xml zerosbk`) → `call_tegrasign(..., list_val='images_list.xml', sha='sha512')` → `tegraflash_update_images()` (line 2860, runs `tegrahost_v2 --partitionlayout ... --updatesig images_list_signed.xml`).

The Boot ROM (BR) BCT is signed separately by `tegraflash_update_br_bct_bl_info()` (line 1347), described in section 4.

`call_tegrasign()` (line 4272) is a thin wrapper that calls the in-process `tegrasign()` function from `tegrasign_v3.py`; tegrasign is imported as a Python module, not exec'd as a subprocess.

The `--key None` path: callers pass the literal Python string `'None'` (not `None`) as the key when they want the zero-SBK behaviour explicitly, e.g. the BR-BCT SHA step at line 1446: `self.call_tegrasign(None, None, None, 'None', None, list_val, ...)`.

---

## 3. What tegrasign computes for zerosbk / no-key

Two routines handle the SBK mode: `sign_single_file()` (internal line 430) for `--file`, and `sign_files_internal()` (internal line 50) for each `<file>` node when `--list` is used. The T264 image and BCT flows always use `--list`, so `sign_files_internal()` is the relevant one; `sign_single_file()` is identical in substance.

For `p_key.mode == NvTegraSign_SBK` (`sign_files_internal`, lines 95-133):

```python
NumAesBlocks = int(length/AES_128_HASH_BLOCK_LEN)   # AES_128_HASH_BLOCK_LEN = 16
length = int(NumAesBlocks * AES_128_HASH_BLOCK_LEN) # truncate length to a multiple of 16

buff_hash = '0' * AES_128_HASH_BLOCK_LEN            # 16 ASCII '0' chars, placeholder
buff_enc = bytearray(buff_to_sign)

if (skip_enc or is_zero_aes(p_keylist[key_index])): # zerosbk => is_zero_aes() True
    info_print('Skipping encryption: ' + filename, True)
else:
    buff_enc = do_aes_cbc(...)                       # NOT taken in ODM-open

if do_sign:
    buff_hash = do_aes_cmac(buff_enc, length, p_keylist[key_index])

buff_data = buff_data[0:offset] + buff_enc + buff_data[offset+length:]
# write *_encrypt.* (== original bytes, since enc skipped) and *.hash (== the CMAC)
perform_sha(filename, filenode.find('sha'))          # only if a <sha> node is present
```

Key facts for ODM-open:

- **Encryption is skipped** because `is_zero_aes()` is true. `buff_enc` stays equal to the input, and the `*_encrypt.*` file is a byte-for-byte copy of the input image.
- **The "signature" is AES-CMAC over the (unencrypted) image bytes using the all-zero 16-byte AES key.** `do_aes_cmac()` (internal line 581) writes a request blob and shells out to the `tegraopenssl` helper with `--aescmac`. The result is a 128-bit (16-byte) value written to the `*.hash` file. With a zero key this CMAC is deterministic but cryptographically meaningless — it is not verified by the Boot ROM in ODM-open. It is computed only because the signing XML sets `sign='1'`.
- **`length` is truncated down to a multiple of 16 bytes** before the CMAC (`NumAesBlocks * 16`). A reimplementation must replicate this truncation.
- The hash algorithm here is **AES-CMAC**, not SHA. There is no SHA256/SHA512 at this step unless the `<file>` node carries a `<sha>` child (`perform_sha`), which the per-image XML produced by `tegraflash_oem_sign_file` does **not** set. The SHA digest that actually matters is computed later by `tegrahost_v2` (see section 5) or, for BR-BCT, by a separate explicit SHA-512 tegrasign call.

The byte range: `buff_to_sign = buff_data[offset : offset + length]`, where `offset`/`length` come from the XML `<file>` node. In `tegraflash_oem_sign_file` these are `self.bch_offset` and `self.bch_length` (the fixed BCH offsets), so the CMAC covers the image body after the header region.

For completeness, `NvTegraSign_FSKP` (Factory Secure Key Provisioning, lines 135-172) is the same logic but with a placeholder of `"0" * 16` and `AES_256_HASH_BLOCK_LEN` (also 16); FSKP is not used in the plain ODM-open coldboot path.

### Per image type

The per-image-type behaviour does **not** diverge in zerosbk mode. mb1, psc_bl1, applet/mb2, MB2-BCT, PCT, etc. all go through `tegraflash_oem_sign_file` → `sign_files_internal` with `mode == SBK`, all get a zero-key AES-CMAC `*.hash` and an unencrypted `*_encrypt.*` copy, and all then get a SHA written into the BCH by `tegrahost_v2 --updatesigheader ... zerosbk`. The BR-BCT is the one exception: it is signed by `tegrabct_v2`/`tegrahost`'s BCT path and gets an explicit SHA-512 digest (section 4).

---

## 4. The BR-BCT path (explicit SHA-512)

`tegraflash_update_br_bct_bl_info()` (line 1347) signs the BR-BCT. In ODM-open (`--key None`, no key list), the relevant steps are:

1. `tegrabct_v2 --brbct ... --listbct bct_list.xml` produces the list of signable BCT regions (line 1381).
2. `call_tegrasign(..., key_val=signkey, list_val='bct_list.xml', sha='sha512')` (line 1424) runs tegrasign over the list. With no key this produces the zero-CMAC `.hash` per region as in section 3.
3. `tegrabct_v2 --brbct ... --updatesig bct_list_signed.xml` (line 1428) copies signatures back into the BCT.
4. **The integrity digest:** `call_tegrasign(None, None, None, 'None', ..., list_val='bct_list.xml', sha='sha512')` (line 1446) is invoked with key `'None'` and `sha512`, then `tegrabct_v2 --brbct ... --updatesha bct_list_signed.xml` (line 1449) writes the SHA-512 into the BCT.

So for BR-BCT the hash that matters is **SHA-512**, computed via the `<sha>` node in `bct_list.xml` (see `perform_sha`, internal line 28, which reads `digest_type`, `digest_file`, `offset`, `length` from the node) and written into the BCT by `tegrabct_v2 --updatesha`.

---

## 5. Interaction with `tegrahost_v2 --appendsigheader ... zerosbk` and `--updatesigheader`

The order of operations in `tegraflash_oem_sign_file` (line 2412) is:

1. **Align** the file: `tegrahost_v2 --align <file>` (line 2420).
2. **Append the signature header (BCH):** `tegrahost_v2 --magicid <ID> --appendsigheader <file> <mode>` (line 2453), producing `<file>_sigheader.<ext>`. For ODM-open `mode` is `zerosbk` (from `tegrasign_values['--mode']`). `addBch()` (line 893) does the same with the literal `'zerosbk'` argument (line 908). **At this point tegrahost has reserved the BCH and recorded stage2 / hash metadata, but the hash field is not yet filled with the real digest.**
3. **Build the signing XML** (`<file>_list.xml`, lines 2465-2490): one `<file>` node with the fixed BCH `offset`/`length`, an `<sbk encrypt='1' sign='1' hash='..._sigheader.<ext>.hash'>` child, plus `pkc`/`ec`/`ec521`/`eddsa`/`xmss` nodes (which carry `digest_type='sha512'` for the asymmetric algorithms).
4. **Run tegrasign** over that XML with `sha='sha512'` (line 2502). In zerosbk mode this writes the zero-key AES-CMAC into `..._sigheader.<ext>.hash` and the (unencrypted) `..._sigheader.<ext>.encrypt`. tegrasign also stamps `mode='sbk'` onto the signed XML root.
5. **Read back the mode** from `<file>_list_signed.xml` (line 2507). Since mode is `sbk` (not in `algo_list`), the code selects (lines 2513-2516):
   ```python
   list_text = "encrypt_file"   # the *_encrypt file is the payload tegrahost consumes
   sig_type  = "zerosbk"
   sig_file  = "hash"           # the CMAC hash file
   ```
6. **Update the signature header:** `tegrahost_v2 [--pubkeyhash pub_key.key] --updatesigheader <signed_file> <sig_file> zerosbk` (line 2528). **This is the step that matters.** `tegrahost_v2`, given `zerosbk`, computes the image SHA and writes it (and the zero CMAC) into the BCH, finalizing the header. tegrasign computed the (irrelevant) CMAC; tegrahost computes and writes the SHA that the loader checks.

For bulk partitions the same two-phase pattern applies at list scope: `tegraflash_generate_signing_list()` runs `--partitionlayout ... --list images_list.xml zerosbk` (appends headers for all images), tegrasign signs `images_list.xml`, and `tegraflash_update_images()` runs `--partitionlayout ... --updatesig images_list_signed.xml` to write the per-image digests back.

Summary of division of labour:

| Step | Tool | What it does in zerosbk |
|------|------|--------------------------|
| `--align` | tegrahost_v2 | Pads file to alignment |
| `--appendsigheader ... zerosbk` | tegrahost_v2 | Reserves/forms the 400-byte BCH, records stage2 metadata |
| `tegrasign` over the XML | tegrasign_v3 | Computes zero-key AES-CMAC → `*.hash` (cosmetic); copies image → `*_encrypt` |
| `--updatesigheader ... zerosbk` | tegrahost_v2 | Computes the real image SHA and writes it (plus the zero CMAC) into the BCH |

---

## 6. Outputs and file/XML formats

`tegrasign_v3` never modifies the input image in place. It writes side files and, for `--list`, a signed XML manifest.

**Side files (per `<file>` node, SBK mode):**
- `<base>_encrypt.<ext>` — the encrypted payload. In ODM-open this is a byte-identical copy of the input (encryption skipped). This is the file tegrahost later consumes as `encrypt_file`.
- `<base>.hash` (or the XML's `hash=` name) — the 16-byte AES-CMAC. Zero-key, hence cosmetic.

**Signed XML manifest:** `sign_files_in_list()` (internal line 386) parses the input list XML, signs each `<file>`, sets `root.set('mode', get_mode_str(...))` (which is `sbk` for zerosbk), prepends an `<?xml ...?>` + `Auto generated by tegrasign` comment, and writes `<name>_signed.xml` (the input name with `.xml` replaced by `_signed.xml`). The known instances:
- `images_list.xml` → `images_list_signed.xml`
- `bct_list.xml` → `bct_list_signed.xml`
- `<file>_aligned_sigheader.<ext>_list.xml` → `..._list_signed.xml` (one-off binaries)

Input node format (documented in the source at internal line 43):

```xml
<file name="rcm_0.rcm" offset="1312" length="160" id="0" type="rcm">
    <sbk encrypt="1" sign="1" encrypt_file="rcm_0_encrypt.rcm" hash="rcm_0.hash"></sbk>
    <pkc signature="rcm_0.sig" signed_file="rcm_0_signed.rcm"></pkc>
    <ec  signature="rcm_0.sig" signed_file="rcm_0_signed.rcm"></ec>
    <eddsa signature="rcm_0.sig" signed_file="rcm_0_signed.rcm"></eddsa>
</file>
```

A `<sha digest_type="sha512" digest_file="...sha" offset="68" length="4028"/>` child triggers `perform_sha` (used by the BCT list, not the per-image list).

In SBK mode tegrasign reads `encrypt_file`/`hash` from the `<sbk>` node and writes those files; it does not touch `signature`/`signed_file` (those are for the asymmetric modes). For ECC/EdDSA/PKC modes tegrasign instead writes a `.sig` signature and a `_signed` file and leaves the `<sbk>` node alone.

The actual SHA digest the loader checks is written into the BCH inside the image by `tegrahost_v2 --updatesigheader` (images) or into the BCT by `tegrabct_v2 --updatesha` (BR-BCT) — neither of these touches the tegrasign manifest contents.

---

## 7. Is the cryptographic signature a no-op for ODM-open?

**Yes.** In ODM-open / non-secure / zerosbk:

- There is no asymmetric signature (no RSA/ECC/EdDSA `.sig` is produced; those branches are not taken because `mode == 'sbk'`).
- The symmetric "signature" is an AES-CMAC under a 16-byte all-zero key. It is computed and written to `*.hash` purely because the XML sets `sign='1'`, but with a zero key it provides no authentication and is not enforced by the Boot ROM in ODM-open. It is cosmetic.
- Encryption is skipped entirely (`is_zero_aes()` short-circuits `do_aes_cbc`), so `*_encrypt.*` equals the plaintext image.

**What is actually required:** the **integrity hash that `tegrahost_v2`/`tegrabct_v2` writes into the BCH/BCT**. For images that is the SHA computed during `--updatesigheader ... zerosbk`; for the BR-BCT it is the SHA-512 written by `--updatesha`. The Boot ROM and early bootloaders in ODM-open verify these digests for image integrity (corruption detection), not authenticity. A correct image must therefore carry a valid SHA in its BCH; a wrong or absent SHA will be rejected, whereas the zero CMAC and the encryption step can be ignored.

For a Go reimplementation, the minimal correct behaviour for ODM-open is:

1. For each image: align, build/append the BCH with `magicid` and `zerosbk` mode, then compute the SHA over the BCH-defined range and write it into the BCH's hash field (the equivalent of `tegrahost_v2 --updatesigheader ... zerosbk`). The AES-CMAC zero "signature" can be written as 16 zero bytes or skipped, since it is not enforced.
2. For the BR-BCT: compute SHA-512 over the listed regions and write it via the BCT's `--updatesha` equivalent.

The exact SHA variant and byte ranges that `tegrahost_v2`/`tegrabct_v2` use for the BCH and BCT are defined by those tools (binary, `tegrabl_sigheader.h`), not by tegrasign; tegrasign's role in ODM-open is limited to producing the (ignorable) zero CMAC and the manifest plumbing.
