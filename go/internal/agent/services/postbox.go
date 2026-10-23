package services

import (
	"bytes"
	"context"
	"crypto/mldsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/wendylabsinc/wendy/go/internal/agent/devicesign"
	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	cloudpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb/v2"
)

// Postbox is the device half of the AAA contract section 5.3 postbox: it pulls
// this device's operator-signed change requests from cloud over the device's
// own mTLS, verifies every entry itself (hash chain, notarized checkpoint,
// high-water mark, operator signature anchored at this device's pinned CAs,
// target, expiry, nonce), applies a verified request through the agent's
// existing paths, reports the outcome on the reported-state stream and signs
// an AckHead for the newest verified entry. Cloud is only a queue: nothing is
// applied on its word, and every refusal is named, logged, counted and
// reported, never skipped. Every failure keeps what is running.
type Postbox struct {
	logger     *zap.Logger
	pub        TelemetryPublisher // nil: no metric
	configPath string
	control    postboxControl
	poll       time.Duration
	retry      time.Duration
	now        func() time.Time
	dial       func(cloudURL, certPEM, keyPEM, chainPEM string) (cloudpbv2.DevicePostboxServiceClient, io.Closer, error)
	// checkpoint verifies PullChangesResponse.checkpoint and returns the
	// notarized head it binds. It is a seam: see refuseCheckpoint.
	checkpoint func(raw []byte, tenant, target string, now time.Time) (maxID int64, headHash []byte, err error)

	wake            chan struct{}
	identityChanged chan struct{}

	mu                                  sync.Mutex
	cloudURL, certPEM, keyPEM, chainPEM string
	state                               *postboxState
	refusals                            map[string]int64
}

// postboxControl is the agent's existing container control path, the one
// the CLI's start/stop calls take.
type postboxControl interface {
	StartApp(ctx context.Context, name string) error
	StopApp(ctx context.Context, name string) error
}

// postboxState is everything that must survive a restart. It belongs to one
// chain: a new device identity starts a new one.
type postboxState struct {
	Principal string `json:"principal"`
	// HighWater is the newest entry verified, HeadHash its entry_hash.
	HighWater int64  `json:"highWater"`
	HeadHash  []byte `json:"headHash,omitempty"`
	// Acked is the newest entry cloud accepted an AckHead for; HighWater above
	// it is a pending ack.
	Acked int64 `json:"acked"`
	// Nonces maps each claimed request nonce to its expiry (unix seconds); an
	// entry is dropped once expired, since an expired request is refused anyway.
	Nonces      map[string]int64              `json:"nonces,omitempty"`
	Assignments map[string]*postboxAssignment `json:"assignments,omitempty"` // by deployment id
	Outcomes    map[string]*postboxOutcome    `json:"outcomes,omitempty"`    // by app id
}

// postboxAssignment is a verified DeviceAssignment held as desired state; the
// registry pull that acts on it is T14.
type postboxAssignment struct {
	AppID           string `json:"appId"`
	AppReleaseID    string `json:"appReleaseId"`
	ManifestDigest  string `json:"manifestDigest"`
	DeploymentKeyID string `json:"deploymentKeyId"`
	DesiredState    string `json:"desiredState"`
}

// postboxOutcome is the latest postbox decision about an app, carried on the
// reported-state stream.
type postboxOutcome struct {
	EntryID       int64                  `json:"entryId"`
	CorrelationID string                 `json:"correlationId"`
	Outcome       cloudpbv2.ApplyOutcome `json:"outcome"`
}

const (
	postboxStateFile = "postbox.json"
	postboxPageSize  = 100
	postboxMaxJWS    = 64 << 10
	postboxMaxSkew   = 60 // seconds an iat may run ahead of this clock
	// postboxMaxLifetime caps expiry - iat (the CLI signs for 30 s). With
	// expiry checked against this clock it also keeps a backdated iat, at
	// which the operator chain is verified, near the present.
	postboxMaxLifetime = 10 * 60
	postboxAudience    = "https://cloud.wendy.sh/broker"
	// materialPending is the one T14 seam's answer until the registry pull exists.
	materialPending = "material pull pending (T14)"
)

// postboxOperations maps each postbox-relayed method (the descriptor's
// operation) to the payload_type it must carry.
var postboxOperations = map[string]string{
	strings.TrimPrefix(cloudpbv2.DeploymentService_CreateDeployment_FullMethodName, "/"):     "wendycloud.v2.CreateDeploymentRequest",
	strings.TrimPrefix(cloudpbv2.DeploymentService_ControlContainer_FullMethodName, "/"):     "wendycloud.v2.ControlContainerRequest",
	strings.TrimPrefix(cloudpbv2.ArtifactRegistryService_AssignDevice_FullMethodName, "/"):   "wendycloud.v2.DeviceAssignment",
	strings.TrimPrefix(cloudpbv2.ArtifactRegistryService_UnassignDevice_FullMethodName, "/"): "wendycloud.v2.UnassignDeviceRequest",
}

// NewPostbox builds the client; it pulls nothing until UpdateIdentity supplies
// an enrolled identity (the agent passes its boot identity straight away).
func NewPostbox(logger *zap.Logger, pub TelemetryPublisher, configPath string, control postboxControl) *Postbox {
	return &Postbox{
		logger: logger, pub: pub, configPath: configPath, control: control,
		poll:            5 * time.Minute, // the nudge is the fast path; this is the fallback
		retry:           30 * time.Second,
		now:             time.Now,
		dial:            dialPostbox,
		checkpoint:      refuseCheckpoint,
		wake:            make(chan struct{}, 1),
		identityChanged: make(chan struct{}, 1),
		refusals:        map[string]int64{},
	}
}

func dialPostbox(cloudURL, certPEM, keyPEM, chainPEM string) (cloudpbv2.DevicePostboxServiceClient, io.Closer, error) {
	conn, closer, err := dialDeviceCloud(cloudURL, certPEM, keyPEM, chainPEM)
	if err != nil {
		return nil, nil, err
	}
	return cloudpbv2.NewDevicePostboxServiceClient(conn), closer, nil
}

// errCheckpointContractGap: service-protos carries no conformance vector for
// the notarized checkpoint artifact (pki-core's {body, signature,
// timestamp_token} with an RFC 3161 token), so the device cannot verify it.
// Without a verified checkpoint nothing is applied (contract section 5.3:
// stale or missing checkpoint = hold and alert), never a weaker check.
var errCheckpointContractGap = errors.New("checkpoint verification has no contract vector yet (CONTRACT-GAP); holding")

func refuseCheckpoint([]byte, string, string, time.Time) (int64, []byte, error) {
	return 0, nil, errCheckpointContractGap
}

// UpdateIdentity swaps the device identity; empty values stop pulling.
func (p *Postbox) UpdateIdentity(cloudURL, certPEM, keyPEM, chainPEM string) {
	p.mu.Lock()
	p.cloudURL, p.certPEM, p.keyPEM, p.chainPEM = cloudURL, certPEM, keyPEM, chainPEM
	p.mu.Unlock()
	select {
	case p.identityChanged <- struct{}{}:
	default:
	}
}

// Nudge is the head cloud announced on the reported-state stream. It carries
// no authority: it only wakes a pull, which verifies everything it gets.
func (p *Postbox) Nudge(head int64) {
	p.mu.Lock()
	behind := p.state == nil || head > p.state.HighWater
	p.mu.Unlock()
	if !behind {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run pulls until ctx is done: on a nudge, on an identity change and on the
// poll timer, backing off with jitter after a failure.
func (p *Postbox) Run(ctx context.Context) {
	failures := 0
	for {
		wait := p.poll
		if err := p.cycle(ctx); err != nil && ctx.Err() == nil {
			failures++
			p.logger.Warn("postbox pull failed", zap.Error(err))
			wait = min(p.retry<<min(failures-1, 5), 30*time.Minute)
			wait = wait/2 + rand.N(wait/2+1) // a fleet does not retry in one wave
		} else {
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case <-p.identityChanged:
		case <-time.After(wait):
		}
	}
}

// postboxIdentity is this device as its own leaf proves it.
type postboxIdentity struct {
	tenant, uuid, uri string
	anchors           []*x509.Certificate
	signer            *devicesign.Signer // nil: cannot ack (P-256 device)
}

func (p *Postbox) identity() (*postboxIdentity, string, string, string, string, error) {
	p.mu.Lock()
	cloudURL, certPEM, keyPEM, chainPEM := p.cloudURL, p.certPEM, p.keyPEM, p.chainPEM
	p.mu.Unlock()
	if certPEM == "" || keyPEM == "" {
		return nil, "", "", "", "", nil
	}
	leaves, err := certs.ParseCertsFromPEM([]byte(certPEM))
	if err != nil || len(leaves) == 0 {
		return nil, "", "", "", "", fmt.Errorf("parsing device certificate: %v", err)
	}
	principal, _ := certs.TenantPrincipalFromCert(leaves[0])
	id, err := certs.ParsePrincipal(principal)
	if err != nil || principal != certs.DeviceSPIFFEURI(id.TenantUUID, id.EntityID) {
		return nil, "", "", "", "", fmt.Errorf("device certificate names %q, not a device principal", principal)
	}
	// The postbox target is the device UUID, never a name (WDY-3919).
	if u, err := uuid.Parse(id.EntityID); err != nil || u.String() != id.EntityID {
		return nil, "", "", "", "", fmt.Errorf("device principal %q is not UUID-keyed; the postbox needs the UUID identity", principal)
	}
	chain, err := certs.ParseCertsFromPEM([]byte(chainPEM))
	if err != nil {
		return nil, "", "", "", "", fmt.Errorf("parsing device CA chain: %w", err)
	}
	ident := &postboxIdentity{tenant: id.TenantUUID, uuid: id.EntityID, uri: principal}
	// The tenant CAs of the device's own pinned chain, never a leaf and never
	// the platform root: anchoring there would let another tenant's operator
	// authority vouch for a signer.
	for _, ca := range chain {
		if ca.BasicConstraintsValid && ca.IsCA && !bytes.Equal(ca.RawSubject, ca.RawIssuer) {
			ident.anchors = append(ident.anchors, ca)
		}
	}
	if len(ident.anchors) == 0 {
		return nil, "", "", "", "", errors.New("device CA chain has no tenant certificate authority to anchor operator signatures")
	}
	if ident.signer, err = devicesign.New(certPEM, keyPEM); err != nil {
		p.logger.Warn("postbox: this device cannot sign AckHead", zap.Error(err))
	}
	return ident, cloudURL, certPEM, keyPEM, chainPEM, nil
}

// cycle pulls every page above the high-water mark, processes what the
// notarized checkpoint covers, then acks the newest verified entry.
func (p *Postbox) cycle(ctx context.Context) error {
	id, cloudURL, certPEM, keyPEM, chainPEM, err := p.identity()
	if id == nil {
		return err
	}
	if err := p.load(id.uri); err != nil {
		return err
	}
	client, closer, err := p.dial(cloudURL, certPEM, keyPEM, chainPEM)
	if err != nil {
		return err
	}
	defer func() { _ = closer.Close() }()

	for {
		hwm := p.snapshot().HighWater
		resp, err := client.PullChanges(ctx, &cloudpbv2.PullChangesRequest{AfterEntryId: hwm, Limit: postboxPageSize})
		if err != nil {
			return fmt.Errorf("pulling postbox: %w", err)
		}
		if len(resp.GetEntries()) == 0 {
			break
		}
		cpMax, cpHead, err := p.checkpoint(resp.GetCheckpoint(), id.tenant, id.uri, p.now())
		if err == nil && cpMax < hwm {
			err = fmt.Errorf("checkpoint max_id %d is below the high-water mark %d", cpMax, hwm)
		}
		if err == nil && cpMax == hwm && hwm > 0 && !bytes.Equal(cpHead, p.snapshot().HeadHash) {
			err = fmt.Errorf("checkpoint head at %d differs from the verified head", hwm)
		}
		if err != nil {
			// Hold: nothing is applied, the mark stays, and it is retried.
			p.count("checkpoint")
			p.logger.Warn("postbox held: checkpoint not verified", zap.Error(err),
				zap.Int64("high_water", hwm), zap.Int("entries_waiting", len(resp.GetEntries())))
			break
		}
		stop := false
		for _, entry := range resp.GetEntries() {
			if entry.GetEntryId() > cpMax {
				stop = true // not notarized yet; the next checkpoint covers it
				break
			}
			if err := p.process(ctx, id, entry, cpMax, cpHead); err != nil {
				p.logger.Error("postbox chain refused; holding", zap.Error(err), zap.Int64("entry_id", entry.GetEntryId()))
				stop = true
				break
			}
		}
		if stop || !resp.GetHasMore() {
			break
		}
	}
	return p.ack(ctx, client, id)
}

// process verifies one entry's chain link and then its request. A chain
// failure is returned: nothing after a broken link can be trusted, so the
// mark stays and the pull holds. A request refusal advances the mark: the
// entry is part of the verified chain, it is just not applied.
func (p *Postbox) process(ctx context.Context, id *postboxIdentity, e *cloudpbv2.PostboxEntry, cpMax int64, cpHead []byte) error {
	st := p.snapshot()
	prev := st.HeadHash
	if st.HighWater == 0 {
		prev = make([]byte, sha256.Size) // genesis
	}
	var chainErr error
	switch want := postboxEntryHash(e.GetPrevHash(), id.tenant, id.uri, e.GetEntryId(), e.GetRequestJws(), e.GetTsrResponse()); {
	case e.GetEntryId() != st.HighWater+1:
		chainErr = fmt.Errorf("entry %d does not follow the high-water mark %d", e.GetEntryId(), st.HighWater)
	case !bytes.Equal(e.GetPrevHash(), prev):
		chainErr = errors.New("prev_hash does not link to the verified head")
	case !bytes.Equal(e.GetEntryHash(), want):
		chainErr = errors.New("entry_hash does not match the entry")
	case e.GetEntryId() == cpMax && !bytes.Equal(want, cpHead):
		chainErr = errors.New("entry is not the head the checkpoint notarized")
	}
	if chainErr != nil {
		p.decide(e, "", claimedApp(e), cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_SIGNATURE, "chain", chainErr)
		return chainErr
	}

	now := p.now()
	req, desc, outcome, reason, verr := verifyPostboxRequest(e, id, now)
	nonceExpiry := int64(0)
	if outcome == cloudpbv2.ApplyOutcome_APPLY_OUTCOME_UNSPECIFIED && verr == nil {
		nonceExpiry = desc.Expiry
	}
	// The entry is verified as part of the chain: claim its nonce and move the
	// mark BEFORE acting, so a crash never applies one signed request twice.
	switch err := p.advance(e, desc.Nonce, nonceExpiry, now); {
	case errors.Is(err, errReplayed):
		outcome, reason, verr = cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_STALE, "replay", err
	case err != nil:
		return err
	}
	if verr != nil {
		p.decide(e, desc.Correlation, claimedApp(e), outcome, reason, verr)
		return nil
	}
	app, outcome, reason, aerr := p.apply(ctx, req)
	p.decide(e, desc.Correlation, app, outcome, reason, aerr)
	return nil
}

// postboxEntryHash is cloud's chain construction (cloud migration 000062
// postbox_entry_hash): SHA-256 over 4-byte big-endian length-prefixed fields.
func postboxEntryHash(prev []byte, tenant, target string, entryID int64, jws, tsr []byte) []byte {
	h := sha256.New()
	for _, f := range [][]byte{prev, []byte(tenant), []byte(target), binary.BigEndian.AppendUint64(nil, uint64(entryID)), jws, tsr} {
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(f))))
		h.Write(f)
	}
	return h.Sum(nil)
}

// postboxDescriptor is the signed request's JCS claims (reqsig v1).
type postboxDescriptor struct {
	Audience    string `json:"aud"`
	BodySHA256  string `json:"body_sha256"`
	Correlation string `json:"correlation_id"`
	Expiry      int64  `json:"expiry"`
	Iat         int64  `json:"iat"`
	Nonce       string `json:"nonce"`
	Operation   string `json:"operation"`
	Target      struct {
		Tenant   string `json:"tenant"`
		CA       string `json:"ca,omitempty"`
		Resource string `json:"resource,omitempty"`
	} `json:"target"`
}

// verifyPostboxRequest checks, in order: the operator signature and its chain
// to this device's pinned CAs at the signed iat (an unverified request's
// claims mean nothing), then the target, then freshness. The nonce is checked
// by the caller against the persisted set. It returns the decoded payload.
func verifyPostboxRequest(e *cloudpbv2.PostboxEntry, id *postboxIdentity, now time.Time) (proto.Message, postboxDescriptor, cloudpbv2.ApplyOutcome, string, error) {
	var desc postboxDescriptor
	sig := func(err error) (proto.Message, postboxDescriptor, cloudpbv2.ApplyOutcome, string, error) {
		return nil, desc, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_SIGNATURE, "signature", err
	}
	compact := string(e.GetRequestJws())
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || len(compact) > postboxMaxJWS {
		return sig(errors.New("request is not a compact JWS"))
	}
	header, err := b64url(parts[0])
	if err != nil {
		return sig(err)
	}
	var h struct {
		Alg string   `json:"alg"`
		X5C []string `json:"x5c"`
	}
	if err := strictJSON(header, &h); err != nil {
		return sig(fmt.Errorf("JWS header: %w", err))
	}
	// Tier A/B: operators sign ML-DSA-65 (HARD rules 1-2); x5c only, never kid.
	if h.Alg != "ML-DSA-65" || len(h.X5C) == 0 {
		return sig(fmt.Errorf("JWS header alg %q with %d x5c entries is not an ML-DSA-65 x5c signature", h.Alg, len(h.X5C)))
	}
	chain := make([]*x509.Certificate, 0, len(h.X5C))
	for _, enc := range h.X5C {
		der, err := b64std(enc)
		if err != nil {
			return sig(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return sig(fmt.Errorf("x5c certificate: %w", err))
		}
		chain = append(chain, cert)
	}
	leaf := chain[0]
	if err := verifyMLDSA65(leaf, []byte(parts[0]+"."+parts[1]), parts[2]); err != nil {
		return sig(err)
	}
	claims, err := b64url(parts[1])
	if err != nil {
		return sig(err)
	}
	if err := strictJSON(claims, &desc); err != nil {
		return sig(fmt.Errorf("JWS claims: %w", err))
	}
	wantType, known := postboxOperations[desc.Operation]
	sum := sha256.Sum256(e.GetRequestPayload())
	switch {
	case desc.Audience != postboxAudience:
		return sig(fmt.Errorf("audience %q", desc.Audience))
	case !known || e.GetPayloadType() != wantType:
		return nil, desc, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_TARGET, "operation",
			fmt.Errorf("operation %q with payload_type %q is not a device action", desc.Operation, e.GetPayloadType())
	case desc.BodySHA256 != b64urlEncode(sum[:]):
		return sig(errors.New("body_sha256 does not match the payload"))
	}
	principal, _ := certs.TenantPrincipalFromCert(leaf)
	signer, err := certs.ParsePrincipal(principal)
	if err != nil || signer.EntityType != certs.EntityUser {
		return sig(fmt.Errorf("signer %q is not an operator or service account", principal))
	}
	// Validity at the signed iat: a request queued while the device was
	// offline stays verifiable after its short-lived operator cert expires.
	at := time.Unix(desc.Iat, 0)
	if _, err := certs.VerifyPeerCertificateChainExpiry(leaf, chain[1:], id.anchors, x509.ExtKeyUsageClientAuth, at, at); err != nil {
		return sig(fmt.Errorf("operator certificate does not chain to this device's pinned CAs at iat: %w", err))
	}

	target := func(err error) (proto.Message, postboxDescriptor, cloudpbv2.ApplyOutcome, string, error) {
		return nil, desc, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_TARGET, "target", err
	}
	if signer.TenantUUID != id.tenant || desc.Target.Tenant != id.tenant {
		return target(fmt.Errorf("signer tenant %q / target tenant %q is not this device's tenant", signer.TenantUUID, desc.Target.Tenant))
	}
	req, err := decodePostboxPayload(e)
	if err != nil {
		return sig(err)
	}
	if err := bindsThisDevice(req, desc.Target.Resource, id); err != nil {
		return target(err)
	}

	stale := func(err error) (proto.Message, postboxDescriptor, cloudpbv2.ApplyOutcome, string, error) {
		return nil, desc, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_REJECTED_STALE, "stale", err
	}
	switch {
	case now.Unix() >= desc.Expiry:
		return stale(fmt.Errorf("request expired at %d", desc.Expiry))
	case desc.Iat > now.Unix()+postboxMaxSkew || desc.Expiry <= desc.Iat || desc.Expiry-desc.Iat > postboxMaxLifetime:
		return stale(fmt.Errorf("request iat %d / expiry %d is not valid now", desc.Iat, desc.Expiry))
	case desc.Nonce == "" || len(desc.Nonce) > 344: // 256 bytes, base64url
		return stale(errors.New("request nonce missing or oversized"))
	}
	if a, ok := req.(*cloudpbv2.DeviceAssignment); ok && a.GetExpiresAt() != nil && !now.Before(a.GetExpiresAt().AsTime()) {
		return stale(fmt.Errorf("assignment expired at %s", a.GetExpiresAt().AsTime()))
	}
	return req, desc, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_UNSPECIFIED, "", nil
}

func decodePostboxPayload(e *cloudpbv2.PostboxEntry) (proto.Message, error) {
	var req proto.Message
	switch e.GetPayloadType() {
	case "wendycloud.v2.CreateDeploymentRequest":
		req = &cloudpbv2.CreateDeploymentRequest{}
	case "wendycloud.v2.ControlContainerRequest":
		req = &cloudpbv2.ControlContainerRequest{}
	case "wendycloud.v2.DeviceAssignment":
		req = &cloudpbv2.DeviceAssignment{}
	case "wendycloud.v2.UnassignDeviceRequest":
		req = &cloudpbv2.UnassignDeviceRequest{}
	default:
		return nil, fmt.Errorf("payload_type %q", e.GetPayloadType())
	}
	if err := proto.Unmarshal(e.GetRequestPayload(), req); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", e.GetPayloadType(), err)
	}
	return req, nil
}

// bindsThisDevice checks that the operator signed this request for this
// device, by the target.resource forms cloud's handlers enforce (cloud
// RequestSigning.md) and the device the signed payload names.
func bindsThisDevice(req proto.Message, resource string, id *postboxIdentity) error {
	org := "org/" + id.tenant + "/"
	switch r := req.(type) {
	case *cloudpbv2.ControlContainerRequest:
		if r.GetAssetId() != id.uuid || r.GetOrganizationId() != id.tenant || resource != "asset/"+id.uuid+"/container/"+r.GetContainerName() {
			return fmt.Errorf("ControlContainer for asset %q (resource %q) is not this device", r.GetAssetId(), resource)
		}
	case *cloudpbv2.DeviceAssignment:
		if r.GetDevice() != id.uri || r.GetOrganizationId() != id.tenant || resource != org+"deployment/"+r.GetDeploymentId()+"/device/"+id.uuid {
			return fmt.Errorf("assignment for %q (resource %q) is not this device", r.GetDevice(), resource)
		}
	case *cloudpbv2.UnassignDeviceRequest:
		if r.GetDevice() != id.uri || r.GetOrganizationId() != id.tenant || resource != org+"deployment/"+r.GetDeploymentId()+"/device/"+id.uuid {
			return fmt.Errorf("unassignment for %q (resource %q) is not this device", r.GetDevice(), resource)
		}
	case *cloudpbv2.CreateDeploymentRequest:
		// The descriptor names the release, not a device. The signed payload
		// binds this device only by its UUID in target_device_ids, or by naming
		// no target at all (every device of the tenant). Tags are resolved by
		// cloud and prove nothing here.
		if r.GetOrganizationId() != id.tenant || resource != org+"release/"+r.GetAppReleaseId() {
			return fmt.Errorf("deployment for organization %q (resource %q) is not this tenant", r.GetOrganizationId(), resource)
		}
		whole := len(r.GetTargetDeviceIds()) == 0 && len(r.GetTargetTags()) == 0
		if !whole && !slices.Contains(r.GetTargetDeviceIds(), id.uuid) {
			return errors.New("deployment does not name this device in its signed targets (tag targeting is cloud-resolved)")
		}
	}
	return nil
}

// apply acts on a verified request through the agent's existing paths. Every
// failure leaves what is running alone and runs nothing new.
func (p *Postbox) apply(ctx context.Context, req proto.Message) (string, cloudpbv2.ApplyOutcome, string, error) {
	switch r := req.(type) {
	case *cloudpbv2.ControlContainerRequest:
		app, _, _ := strings.Cut(r.GetContainerName(), ".")
		var err error
		switch r.GetAction() {
		case cloudpbv2.ContainerAction_CONTAINER_ACTION_START:
			err = p.control.StartApp(ctx, r.GetContainerName())
		case cloudpbv2.ContainerAction_CONTAINER_ACTION_STOP:
			err = p.control.StopApp(ctx, r.GetContainerName())
		default:
			err = fmt.Errorf("unknown container action %v", r.GetAction())
		}
		if err != nil {
			// No ApplyOutcome names a failed apply of a verified request yet.
			return app, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_UNSPECIFIED, "apply", err
		}
		return app, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_APPLIED, "", nil
	case *cloudpbv2.DeviceAssignment:
		p.mu.Lock()
		p.state.Assignments[r.GetDeploymentId()] = &postboxAssignment{
			AppID: r.GetAppId(), AppReleaseID: r.GetAppReleaseId(), ManifestDigest: r.GetManifestDigest(),
			DeploymentKeyID: r.GetDeploymentKeyId(), DesiredState: r.GetDesiredState().String(),
		}
		p.mu.Unlock()
		outcome, reason, err := p.pullMaterial(ctx, r.GetAppReleaseId(), r)
		return r.GetAppId(), outcome, reason, err
	case *cloudpbv2.UnassignDeviceRequest:
		p.mu.Lock()
		app := ""
		if a := p.state.Assignments[r.GetDeploymentId()]; a != nil {
			app = a.AppID
		}
		delete(p.state.Assignments, r.GetDeploymentId())
		p.mu.Unlock()
		return app, cloudpbv2.ApplyOutcome_APPLY_OUTCOME_APPLIED, "", nil
	case *cloudpbv2.CreateDeploymentRequest:
		outcome, reason, err := p.pullMaterial(ctx, r.GetAppReleaseId(), nil)
		return "", outcome, reason, err // names a release, not an app
	}
	return "", cloudpbv2.ApplyOutcome_APPLY_OUTCOME_UNSPECIFIED, "apply", fmt.Errorf("no apply path for %T", req)
}

// pullMaterial is the single seam for the registry pull, unwrap and decrypt
// (T14). Until then a verified deployment or assignment is held as desired
// state and reported, and nothing new runs.
func (p *Postbox) pullMaterial(_ context.Context, releaseID string, _ *cloudpbv2.DeviceAssignment) (cloudpbv2.ApplyOutcome, string, error) {
	return cloudpbv2.ApplyOutcome_APPLY_OUTCOME_PULL_REFUSED, "material", fmt.Errorf("%s for release %q", materialPending, releaseID)
}

// decide audits, counts and records one postbox decision.
func (p *Postbox) decide(e *cloudpbv2.PostboxEntry, correlation, app string, outcome cloudpbv2.ApplyOutcome, reason string, err error) {
	if correlation == "" {
		correlation = peekCorrelation(e.GetRequestJws()) // unverified, for tracing only
	}
	fields := []zap.Field{
		zap.String("audit", "postbox"), zap.Int64("entry_id", e.GetEntryId()), zap.String("correlation_id", correlation),
		zap.String("payload_type", e.GetPayloadType()), zap.String("app_id", app), zap.String("outcome", outcome.String()),
	}
	if err != nil {
		p.count(reason)
		p.logger.Warn("postbox entry refused", append(fields, zap.String("reason", reason), zap.Error(err))...)
	} else {
		p.logger.Info("postbox entry applied", fields...)
	}
	// An outcome not attributable to an app has the audit line as its record.
	if app != "" {
		p.mu.Lock()
		p.state.Outcomes[app] = &postboxOutcome{EntryID: e.GetEntryId(), CorrelationID: correlation, Outcome: outcome}
		p.mu.Unlock()
	}
	if err := p.save(); err != nil {
		p.logger.Warn("postbox: saving outcome failed", zap.Error(err))
	}
}

// Outcomes returns the latest postbox decision per app for the reported state.
func (p *Postbox) Outcomes() map[string]postboxOutcome {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]postboxOutcome{}
	if p.state != nil {
		for app, o := range p.state.Outcomes {
			out[app] = *o
		}
	}
	return out
}

// ack signs PostboxHeadAck for the newest verified entry until cloud takes it.
func (p *Postbox) ack(ctx context.Context, client cloudpbv2.DevicePostboxServiceClient, id *postboxIdentity) error {
	st := p.snapshot()
	if st.HighWater <= st.Acked || id.signer == nil {
		return nil
	}
	signed, err := id.signer.Sign(cloudpbv2.DevicePostboxService_AckHead_FullMethodName, &cloudpbv2.PostboxHeadAck{
		EntryId: st.HighWater, EntryHash: st.HeadHash, ObservedAt: timestamppb.New(p.now()),
	}, uuid.NewString())
	if err != nil {
		return err
	}
	if _, err := client.AckHead(ctx, signed); err != nil {
		return fmt.Errorf("acking postbox head %d: %w", st.HighWater, err)
	}
	p.mu.Lock()
	p.state.Acked = max(p.state.Acked, st.HighWater)
	p.mu.Unlock()
	return p.save()
}

// advance moves the mark past a chain-verified entry and claims its nonce.
func (p *Postbox) advance(e *cloudpbv2.PostboxEntry, nonce string, nonceExpiry int64, now time.Time) error {
	p.mu.Lock()
	st := p.state
	replayed := false
	if nonceExpiry > 0 {
		if _, seen := st.Nonces[nonce]; seen {
			replayed = true
		} else {
			st.Nonces[nonce] = nonceExpiry
		}
	}
	for n, exp := range st.Nonces {
		if exp <= now.Unix() {
			delete(st.Nonces, n)
		}
	}
	st.HighWater, st.HeadHash = e.GetEntryId(), e.GetEntryHash()
	p.mu.Unlock()
	if err := p.save(); err != nil {
		return fmt.Errorf("persisting postbox high-water mark: %w", err)
	}
	if replayed {
		return errReplayed
	}
	return nil
}

var errReplayed = errors.New("nonce replayed")

func (p *Postbox) snapshot() postboxState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return *p.state
}

// load reads the persisted state for principal; another principal's state
// (a re-enrolled device has a new UUID and a new chain) is discarded.
func (p *Postbox) load(principal string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != nil && p.state.Principal == principal {
		return nil
	}
	st := &postboxState{}
	data, err := os.ReadFile(filepath.Join(p.configPath, postboxStateFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return fmt.Errorf("reading postbox state: %w", err)
	default:
		if err := json.Unmarshal(data, st); err != nil {
			return fmt.Errorf("decoding postbox state: %w", err)
		}
	}
	if st.Principal != principal {
		st = &postboxState{Principal: principal}
	}
	if st.Nonces == nil {
		st.Nonces = map[string]int64{}
	}
	if st.Assignments == nil {
		st.Assignments = map[string]*postboxAssignment{}
	}
	if st.Outcomes == nil {
		st.Outcomes = map[string]*postboxOutcome{}
	}
	p.state = st
	return nil
}

func (p *Postbox) save() error {
	p.mu.Lock()
	data, err := json.Marshal(p.state)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	return writeProvisioningFile(p.configPath, postboxStateFile, data)
}

func (p *Postbox) count(reason string) {
	p.mu.Lock()
	p.refusals[reason]++
	n := p.refusals[reason]
	p.mu.Unlock()
	if p.pub == nil {
		return
	}
	p.pub.PublishMetrics(&colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: newAgentResource(),
			ScopeMetrics: []*metricspb.ScopeMetrics{{
				Scope: &commonpb.InstrumentationScope{Name: "wendy.postbox"},
				Metrics: []*metricspb.Metric{{
					Name: "postbox.refusals",
					Unit: "1",
					Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{
						IsMonotonic:            true,
						AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
						DataPoints: []*metricspb.NumberDataPoint{{
							TimeUnixNano: uint64(p.now().UnixNano()),
							Value:        &metricspb.NumberDataPoint_AsInt{AsInt: n},
							Attributes: []*commonpb.KeyValue{{
								Key: "reason", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: reason}},
							}},
						}},
					}},
				}},
			}},
		}},
	})
}

// claimedApp is the app an entry's (possibly unverified) payload names, so a
// refusal is reported against it.
func claimedApp(e *cloudpbv2.PostboxEntry) string {
	req, err := decodePostboxPayload(e)
	if err != nil {
		return ""
	}
	switch r := req.(type) {
	case *cloudpbv2.ControlContainerRequest:
		app, _, _ := strings.Cut(r.GetContainerName(), ".")
		return app
	case *cloudpbv2.DeviceAssignment:
		return r.GetAppId()
	}
	return ""
}

func peekCorrelation(jws []byte) string {
	parts := strings.Split(string(jws), ".")
	if len(parts) != 3 {
		return ""
	}
	claims, err := b64url(parts[1])
	if err != nil {
		return ""
	}
	var d struct {
		Correlation string `json:"correlation_id"`
	}
	_ = json.Unmarshal(claims, &d)
	return d.Correlation
}

// containerControl drives the same ContainerService handlers the CLI calls:
// a start is the CLI's detached start, which returns once the app started.
type containerControl struct{ svc *ContainerService }

// NewPostboxControl adapts the agent's container service for the postbox.
func NewPostboxControl(svc *ContainerService) postboxControl { return containerControl{svc: svc} }

func (c containerControl) StopApp(ctx context.Context, name string) error {
	_, err := c.svc.StopContainer(ctx, &agentpb.StopContainerRequest{AppName: name})
	return err
}

func (c containerControl) StartApp(ctx context.Context, name string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := &detachedStart{ctx: ctx, cancel: cancel}
	err := c.svc.StartContainer(&agentpb.StartContainerRequest{AppName: name}, stream)
	if stream.started {
		return nil // the handler ends with the detached client's cancellation
	}
	if err == nil {
		err = errors.New("container did not report a start")
	}
	return err
}

type detachedStart struct {
	grpc.ServerStream // unused by the start handler; nil
	ctx               context.Context
	cancel            context.CancelFunc
	started           bool
}

func (s *detachedStart) Context() context.Context { return s.ctx }

func (s *detachedStart) Send(m *agentpb.RunContainerLayersResponse) error {
	if m.GetStarted() != nil {
		s.started = true
		s.cancel()
	}
	return nil
}

func b64url(s string) ([]byte, error) { return base64.RawURLEncoding.Strict().DecodeString(s) }

func b64urlEncode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64std(s string) ([]byte, error) { return base64.StdEncoding.Strict().DecodeString(s) }

func strictJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// verifyMLDSA65 checks a JWS signature (raw FIPS 204, empty context, as the
// signers produce it) against an ML-DSA-65 leaf.
func verifyMLDSA65(leaf *x509.Certificate, input []byte, sigB64 string) error {
	pk, ok := leaf.PublicKey.(*mldsa.PublicKey)
	if !ok || pk.Parameters() != mldsa.MLDSA65() {
		return errors.New("signing leaf does not hold an ML-DSA-65 key")
	}
	sig, err := b64url(sigB64)
	if err != nil {
		return err
	}
	return mldsa.Verify(pk, input, sig, nil)
}
