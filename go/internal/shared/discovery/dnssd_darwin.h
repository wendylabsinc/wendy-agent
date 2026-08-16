#ifndef WENDY_DNSSD_DARWIN_H
#define WENDY_DNSSD_DARWIN_H

#include <dns_sd.h>
#include <stdint.h>

// Browse and resolve wrappers around <dns_sd.h>. Each takes an opaque handle
// that identifies the Go-side session; the C reply callbacks forward it back
// to Go. Declarations only — cgo forbids C definitions in the preamble of a
// file that also uses //export.

DNSServiceErrorType wendy_dnssd_browse(DNSServiceRef *ref, const char *regtype,
                                       uintptr_t handle);

DNSServiceErrorType wendy_dnssd_resolve(DNSServiceRef *ref, const char *name,
                                        const char *regtype, const char *domain,
                                        uint32_t interface_index,
                                        uintptr_t handle);

DNSServiceErrorType wendy_dnssd_getaddrinfo(DNSServiceRef *ref,
                                            const char *hostname,
                                            uint32_t interface_index,
                                            uintptr_t handle);

#endif
