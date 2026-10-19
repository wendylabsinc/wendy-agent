package commands

import (
	"context"
	"crypto"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
	"github.com/wendylabsinc/wendy/go/internal/cli/localkey"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/devicepin"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Local-operator rung of the dial ladder (WDY-3882). A device installed with
// owner keys and not cloud-enrolled serves only mTLS, with a certificate
// self-signed over its device key, and admits the self-issued certificates of
// the keys in its trusted-keys file. The CLI's credential for it is the
// dedicated local operator key in the config dir (localkey). A cloud-enrolled
// device refuses local keys, so the ladder tries the cloud operator
// certificates first and only then this rung.

// localOperatorKeyFn loads the local operator key, creating it on first use
// like `wendy os install` and `wendy auth local-key show` do.
var localOperatorKeyFn = func() (crypto.Signer, error) {
	path, err := localkey.Path()
	if err != nil {
		return nil, err
	}
	return localkey.LoadOrCreate(path)
}

// openLocalPinStoreFn opens the SPKI pin store the device key is pinned in.
var openLocalPinStoreFn = func() (*devicepin.Store, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return devicepin.Open(dir)
}

// localDialResult is what the local rung learned when it produced no
// connection.
type localDialResult struct {
	// enrolledSeen: a device answered with a cloud-enrolled certificate.
	enrolledSeen bool
	// pinned: the device key is pinned for this name, so it has been reached
	// over the local-operator path before and must never be reached in the
	// clear.
	pinned bool
	// verifiedProbeErr: a device verified as local-only during the handshake
	// then failed the version probe for a reason other than refusing the key.
	verifiedProbeErr error
	// keyErr: the local operator key could not be loaded or created.
	keyErr error
}

// localKeyRefusedError is a local-only device refusing this computer's local
// key. It reads as unauthorized (errProvisionedAgentUnauthorized), so callers
// stop instead of trying other devices or transports.
type localKeyRefusedError struct{ msg string }

func (e *localKeyRefusedError) Error() string { return e.msg }
func (e *localKeyRefusedError) Is(target error) bool {
	return target == errProvisionedAgentUnauthorized
}

// localPinnedUnreachableError: nothing authenticated answered a name whose
// device key is pinned. Like the cloud pin's refusal it is
// errNoAuthenticatedEndpoint, so no unauthenticated fallback is offered, but
// it is not the type enrollment's trust prompt recovers from.
type localPinnedUnreachableError struct{ msg string }

func (e *localPinnedUnreachableError) Error() string { return e.msg }
func (e *localPinnedUnreachableError) Is(target error) bool {
	return target == errNoAuthenticatedEndpoint
}

// dialLocalOperator tries the local operator key at each candidate's port and
// port+1 (the mTLS port), the same two ports the cloud rung tries, all at once:
// the cloud rung has already waited out unreachable addresses once. It
// returns a live connection (the first in candidate order), or a refusal the
// ladder must return as is (no plaintext), or neither with what it saw.
func dialLocalOperator(ctx context.Context, target dialTarget, candidates []string) (*grpcclient.AgentConnection, localDialResult, error) {
	var res localDialResult
	var pins *devicepin.Store
	// The name dialled first, then the other names the device is known by
	// (the same aliases lookupPin consults). An alias can only add a pin to
	// match, never remove one, so a spoofed alias makes the check stricter.
	names := pinCandidateKeys(target.PinKey)
	if len(names) > 0 {
		if store, err := openLocalPinStoreFn(); err == nil {
			pins = store
			for _, name := range names {
				res.pinned = res.pinned || store.Has(devicepin.LocalPinKey(name))
			}
		}
	}
	var addrs []string
	for _, cand := range candidates {
		host, portStr, err := net.SplitHostPort(cand)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		addrs = append(addrs, cand, hostPort(host, port+1))
	}
	if len(addrs) == 0 || ctx.Err() != nil {
		return nil, res, ctx.Err()
	}
	key, err := localOperatorKeyFn()
	if err != nil {
		// No usable key (a group-readable file, or sudo before one exists):
		// the rung cannot run. The ladder's other refusals still apply, and
		// the reason is reported with whatever the dial ends in.
		res.keyErr = err
		return nil, res, nil
	}
	// A device pinned to a cloud identity answering as a locally owned one
	// is refused below, so its key must not be recorded either. The store is
	// not safe for concurrent use; the handshakes below run in parallel.
	var check func(*x509.Certificate) error
	if pins != nil && !target.pinned() {
		var mu sync.Mutex
		check = func(leaf *x509.Certificate) error {
			mu.Lock()
			defer mu.Unlock()
			return pins.CheckLocal(names, leaf)
		}
	}
	type attempt struct {
		conn *grpcclient.AgentConnection
		resp *agentpb.GetAgentVersionResponse
		err  error
	}
	attempts := make([]attempt, len(addrs))
	var wg sync.WaitGroup
	for i, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := grpcclient.ConnectWithLocalKey(ctx, addr, key, check)
			if err != nil {
				attempts[i].err = err
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, mtlsProbeTimeout)
			defer cancel()
			attempts[i].conn = conn
			attempts[i].resp, attempts[i].err = conn.AgentService.GetAgentVersion(probeCtx, &agentpb.GetAgentVersionRequest{})
		}()
	}
	wg.Wait()

	var winner *grpcclient.AgentConnection
	var refusal error
	keyRefused, keyClosed := false, false
	for i, a := range attempts {
		conn := a.conn
		if conn == nil {
			continue
		}
		if a.err == nil {
			if winner == nil && refusal == nil {
				winner = conn
				conn.CacheAgentVersion(a.resp)
			} else {
				conn.Close()
			}
			continue
		}
		conn.Close()
		if pm, ok := pinMismatchFn(conn); ok && refusal == nil {
			// Name the key the pin is filed under: that is what unpin clears.
			refusal = refuseIdentity(
				"device %q presented a different device key than the one pinned for %q when this computer first reached it (pinned %s, now %s); refusing to connect — if the device was reinstalled, run 'wendy device unpin %s'",
				target.PinKey, pm.DisplayName, pm.Want, pm.Got, strings.TrimPrefix(pm.Key, "local:"))
		}
		if conn.NotLocalDevice() {
			res.enrolledSeen = true
		}
		// Only a device that proved itself local-only (its certificate
		// verified) and then failed the handshake refused our key.
		if conn.LocalDeviceVerified() {
			switch {
			case isCertRejectionError(addrs[i], a.err):
				keyRefused = true
			case status.Code(a.err) == codes.Unavailable:
				// The device closed the connection right after its verified
				// handshake: on loopback and some stacks that is how a
				// refused client certificate shows instead of an alert.
				keyClosed = true
			case res.verifiedProbeErr == nil:
				// The device proved itself; whatever failed after the
				// handshake, plaintext is not an option for it.
				res.verifiedProbeErr = mtlsAttemptError{addr: addrs[i], err: a.err}
			}
		}
	}
	if refusal != nil {
		if winner != nil {
			winner.Close()
		}
		return nil, res, refusal
	}
	if winner != nil {
		if target.pinned() {
			winner.Close()
			return nil, res, refuseIdentity(
				"device %q is pinned to a cloud-enrolled identity but now answers as a locally owned device; refusing to connect — if it was unenrolled or reinstalled, run 'wendy device unpin %s'",
				target.refusalKey(), target.refusalKey())
		}
		return winner, res, nil
	}
	if keyRefused || keyClosed {
		fp, _ := localkey.Fingerprint(key)
		lead := fmt.Sprintf("device %q accepts only trusted operator keys and does not trust this computer's local operator key (%s). ", targetName(target), fp)
		if !keyRefused {
			lead = fmt.Sprintf("device %q accepts only trusted operator keys and closed the connection after the TLS handshake, which is how it refuses a key it does not trust (this computer's local operator key is %s). ", targetName(target), fp)
		}
		return nil, res, &localKeyRefusedError{msg: lead +
			"Ask an owner of the device to add it: they run 'wendy device keys add <key file> --role operator' with the output of 'wendy auth local-key show' from this computer. " +
			"If you installed the device yourself with another computer, use that computer or reinstall the device."}
	}
	return nil, res, ctx.Err()
}

// targetName is the name a message about this dial uses.
func targetName(t dialTarget) string {
	if t.PinKey != "" {
		return t.PinKey
	}
	return t.Addr
}

// localDialRefusal is the ladder's verdict after every authenticated rung
// failed, for what the local rung saw: nil means the plaintext rung may run.
// verifiedProbeErr is a cloud rung's equivalent of res.verifiedProbeErr.
func localDialRefusal(target dialTarget, res localDialResult, haveCloudCerts bool, verifiedProbeErr error) error {
	if verifiedProbeErr == nil {
		verifiedProbeErr = res.verifiedProbeErr
	}
	var msg string
	switch {
	case verifiedProbeErr != nil:
		return &verifiedDeviceUnresponsiveError{msg: verifiedProbeMessage(targetName(target), verifiedProbeErr)}
	case res.pinned:
		msg = fmt.Sprintf(
			"device %q is a locally owned device this computer has reached with its local operator key before, and no authenticated endpoint answered at %s; refusing to connect without authentication. Check that the device is on and reachable.",
			target.PinKey, target.Addr)
		if res.keyErr != nil {
			msg += fmt.Sprintf(" The local operator key could not be used: %v.", res.keyErr)
		}
		return &localPinnedUnreachableError{msg: msg}
	case res.enrolledSeen && !haveCloudCerts:
		return &noCloudCredentialError{msg: fmt.Sprintf(
			"device %q is enrolled in Wendy Cloud and only accepts operator certificates of its organization; local operator keys are not used while a device is cloud-enrolled. Run 'wendy auth login' for the device's organization.",
			targetName(target))}
	}
	return nil
}

// verifiedDeviceUnresponsiveError: a device authenticated itself over TLS
// (cloud chain or local key) and then failed the version probe. It is
// errNoAuthenticatedEndpoint: no unauthenticated transport may stand in for
// a device that has just proved it speaks TLS.
type verifiedDeviceUnresponsiveError struct{ msg string }

func (e *verifiedDeviceUnresponsiveError) Error() string { return e.msg }
func (e *verifiedDeviceUnresponsiveError) Is(target error) bool {
	return target == errNoAuthenticatedEndpoint
}

func verifiedProbeMessage(name string, err error) string {
	next := "Retry; if it persists, check the network path to the device."
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		next = "The device may be busy or slow to answer; retry."
	case codes.Unimplemented:
		next = "Its agent does not serve the version check; update it with 'wendy device update' or reinstall it."
	}
	return fmt.Sprintf("device %q authenticated over TLS but did not answer the version check (%v); refusing to fall back to an unauthenticated connection. %s", name, err, next)
}

// noCloudCredentialError: a cloud-enrolled device answered and the CLI holds
// no operator certificate to offer it.
type noCloudCredentialError struct{ msg string }

func (e *noCloudCredentialError) Error() string { return e.msg }
func (e *noCloudCredentialError) Is(target error) bool {
	return target == errProvisionedAgentUnauthorized
}
