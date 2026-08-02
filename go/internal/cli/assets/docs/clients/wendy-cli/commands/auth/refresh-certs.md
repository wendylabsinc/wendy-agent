Generates a new key pair and reissues mTLS certificates for each stored auth entry, replacing the existing certificates in place. Useful when certificates are close to expiry.

The refreshed CSR carries the same authoritative identity URN
(`urn:wendy:org:‹org›:user:‹userID›` for user sessions,
`urn:wendy:org:‹org›:asset:‹assetID›` for device sessions) as a URI Subject
Alternative Name, derived from the org, user, and asset fields stored in
`~/.wendy/config.json`. Legacy session entries that predate org-scoped identity
(no `org_id`) refresh with a CommonName-only CSR and receive no URI SAN.
