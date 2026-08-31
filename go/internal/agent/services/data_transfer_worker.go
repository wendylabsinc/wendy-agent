package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/wendylabsinc/wendy/go/internal/agent/data"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

const (
	// transferChunkBytes is the payload size per UploadEpisodeChunk message. The
	// cloud ingest contract caps a chunk at 1 MiB of data; we send exactly that
	// (except the final short chunk of each file).
	transferChunkBytes = 1 << 20

	// transferMaxAttempts bounds how many times a single episode is retried on
	// retryable (transport) failures before the worker gives up and records the
	// episode as permanently "failed". Verification failures are terminal on the
	// first occurrence and do not consume the retry budget.
	transferMaxAttempts = 5

	// transferMaxBackoff caps the per-pass exponential backoff between failed
	// upload passes, matching the telemetry flusher's ceiling.
	transferMaxBackoff = 60 * time.Second

	// transferIdlePause is the pause between successful passes when there is no
	// backlog, to avoid busy-looping the disk scan.
	transferIdlePause = 10 * time.Second
)

// Upload workflow states persisted in Manifest.Upload.State. These mirror the
// vocabulary the data manager already understands (see awaitingUpload).
const (
	uploadStatePending   = "pending"
	uploadStateUploading = "uploading"
	uploadStateUploaded  = "uploaded"
	uploadStateFailed    = "failed"
)

// ingestClientFactory produces a DataIngestService client bound to the device's
// asset identity. It returns the org and asset IDs the identity asserts (so the
// worker can populate the manifest the server cross-checks against the cert),
// plus a closeFn the caller must invoke when the pass is done. Production dials
// the cloud over mTLS; tests inject an in-process client.
type ingestClientFactory func(ctx context.Context) (client cloudpb.DataIngestServiceClient, closeFn func(), err error)

// DataTransferWorker uploads sealed episodes to the cloud DataIngestService. It
// consumes Manifest.Upload as a durable queue: episodes marked "pending" (and,
// after a crash, "uploading") are streamed to the cloud and the manifest is
// resealed at every state transition, so the worker is at-least-once and
// crash-safe. It connects to the same cloud host as the telemetry flusher using
// the same asset mTLS identity, over a distinct DataIngestService client.
type DataTransferWorker struct {
	logger          *zap.Logger
	manager         *data.Manager
	provisioningSvc *ProvisioningService // nil in tests
	factory         ingestClientFactory  // set in tests; production builds one from provisioningSvc

	maxAttempts int
	// ingestHostOverride, when non-empty, replaces the provisioning cloud host
	// as the DataIngestService dial target. Enrollment is still required (the
	// asset identity and client certificate come from provisioning); only the
	// destination changes. Set from WENDY_DATA_INGEST_URL in main.
	ingestHostOverride string
	// onWiFi reports whether the device is currently on Wi-Fi, gating campaigns
	// whose upload.when is "wifi". When nil, no network-type signal is wired and
	// "wifi" is treated as "always" (see resolveShouldUpload).
	onWiFi func() bool
	// now and newSleeper are injection points for deterministic tests; both
	// default to real time.
	now        func() time.Time
	newSleeper func(ctx context.Context) func(time.Duration)
}

// NewDataTransferWorker builds a worker that reads cloud credentials and the
// asset identity from the ProvisioningService at run time, dialling the cloud
// over mTLS once per pass (mirroring CloudFlusher).
func NewDataTransferWorker(logger *zap.Logger, manager *data.Manager, provisioningSvc *ProvisioningService) *DataTransferWorker {
	w := &DataTransferWorker{
		logger:          logger,
		manager:         manager,
		provisioningSvc: provisioningSvc,
		maxAttempts:     transferMaxAttempts,
		now:             time.Now,
		newSleeper:      contextSleeper,
	}
	w.factory = w.dialFactory
	return w
}

// SetIngestHostOverride points the worker's uploads at host instead of the
// provisioning cloud host. host may be a bare host, host:port, or an
// http(s):// URL; the scheme and any path are stripped and the default port
// (443) is applied by the dialer. An empty host clears the override.
func (w *DataTransferWorker) SetIngestHostOverride(host string) {
	w.ingestHostOverride = normalizeIngestOverride(host)
}

// normalizeIngestOverride reduces an override value to the host[:port] form
// dialCloudMTLS expects, tolerating URL-shaped input such as the Cloud Run
// service URL copied from gcloud.
func normalizeIngestOverride(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(host, "https://")
	host = strings.TrimPrefix(host, "http://")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	return host
}

// ingestDialHost resolves the host the current pass should dial: the override
// when set, the enrolled cloud host otherwise.
func (w *DataTransferWorker) ingestDialHost(cloudHost string) string {
	if w.ingestHostOverride != "" {
		return w.ingestHostOverride
	}
	return cloudHost
}

// ingestCertIdentityHeader carries a certificate identity in the URI= form the
// cloud's Envoy certificate metadata extractor reads. The extractor PREFERS
// this header over the X-Forwarded-Client-Cert (XFCC) header that Envoy itself
// injects, and production Envoy does not strip a client-supplied one, so a
// device that sent this header on the normal enrolled path would be
// self-asserting its identity. It must therefore only ever be attached on the
// override path, which does not go through Envoy at all.
const ingestCertIdentityHeader = "x-wendy-client-cert"

// ingestDialOptions returns the extra dial options for this pass.
//
// On the normal enrolled path it returns nil, so no certificate identity
// header is attached: Wendy's Envoy ingress terminates mutual Transport Layer
// Security (mTLS) in front of the broker and injects the identity itself.
//
// With an ingest host override in effect there is no Envoy ingress in front of
// the endpoint to do that, so the worker attaches the identity its enrolled
// asset certificate asserts (the same URI form the cross-repo end-to-end
// harness injects). orgID and assetID must come from ProvisioningInfo.
func (w *DataTransferWorker) ingestDialOptions(orgID, assetID int32) []grpc.DialOption {
	if w.ingestHostOverride == "" {
		return nil
	}
	return certIdentityDialOptions(orgID, assetID)
}

// certIdentityDialOptions attaches the asset's certificate identity to every
// unary call and stream on the connection. Shared by the episode transfer
// worker and the telemetry flusher, which face the same problem on their
// override paths: the endpoint has no Envoy ingress in front of it to inject
// the identity, so the client must assert the one its enrolled certificate
// already carries. Callers must only use this on an override path, and must
// take orgID and assetID from ProvisioningInfo.
func certIdentityDialOptions(orgID, assetID int32) []grpc.DialOption {
	header := fmt.Sprintf("URI=urn:wendy:org:%d:asset:%d", orgID, assetID)
	withIdentity := func(ctx context.Context) context.Context {
		return metadata.AppendToOutgoingContext(ctx, ingestCertIdentityHeader, header)
	}
	return []grpc.DialOption{
		grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			return invoker(withIdentity(ctx), method, req, reply, cc, opts...)
		}),
		grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			return streamer(withIdentity(ctx), desc, cc, method, opts...)
		}),
	}
}

// contextSleeper returns a sleep function that returns early when ctx is done,
// so backoff and bandwidth pacing stay responsive to shutdown.
func contextSleeper(ctx context.Context) func(time.Duration) {
	return func(d time.Duration) {
		if d <= 0 {
			return
		}
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
	}
}

// dialFactory is the production ingestClientFactory: it waits for provisioning,
// dials the cloud over mTLS, and returns a DataIngestService client.
func (w *DataTransferWorker) dialFactory(ctx context.Context) (cloudpb.DataIngestServiceClient, func(), error) {
	cloudHost, orgID, assetID, enrolled := w.provisioningSvc.ProvisioningInfo()
	if !enrolled {
		return nil, nil, errors.New("data transfer worker: not provisioned")
	}
	certPEM, chainPEM, keyData := w.provisioningSvc.ProvisioningCerts()
	// The identity sent, when one is sent at all, is the one the enrolled asset
	// certificate asserts: it comes from ProvisioningInfo above, never from the
	// environment or from anything an app on the device can influence.
	extraOpts := w.ingestDialOptions(orgID, assetID)
	conn, err := func() (*grpc.ClientConn, error) {
		defer zeroBytes(keyData)
		return dialCloudMTLS(w.ingestDialHost(cloudHost), certPEM, chainPEM, keyData, extraOpts...)
	}()
	if err != nil {
		return nil, nil, err
	}
	return cloudpb.NewDataIngestServiceClient(conn), func() { _ = conn.Close() }, nil
}

// Run drives the transfer worker until ctx is cancelled. It waits for the agent
// to be provisioned, then continuously drains the upload backlog with
// exponential backoff between failed passes (1s to 60s). It blocks; start it in
// a goroutine.
func (w *DataTransferWorker) Run(ctx context.Context) {
	if w.factory == nil || w.manager == nil {
		return
	}
	if w.provisioningSvc != nil {
		for {
			if _, _, _, enrolled := w.provisioningSvc.ProvisioningInfo(); enrolled {
				break
			}
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}

	w.logger.Info("data transfer worker: started")
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := w.runPass(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Warn("data transfer worker: pass failed", zap.Error(err))
			w.backoff(ctx, attempt)
			if attempt < 6 { // 2^6 = 64s > 60s cap
				attempt++
			}
			continue
		}
		attempt = 0
		select {
		case <-time.After(transferIdlePause):
		case <-ctx.Done():
			return
		}
	}
}

func (w *DataTransferWorker) backoff(ctx context.Context, attempt int) {
	d := time.Second << attempt
	if d > transferMaxBackoff || d <= 0 {
		d = transferMaxBackoff
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// runPass dials the cloud, enumerates the upload backlog, and uploads each
// episode. A failure to dial or enumerate fails the whole pass (retried with
// backoff); a failure uploading one episode is recorded on that episode's
// manifest and does not abort the pass.
func (w *DataTransferWorker) runPass(ctx context.Context) error {
	client, closeFn, err := w.factory(ctx)
	if err != nil {
		return fmt.Errorf("acquire ingest client: %w", err)
	}
	defer closeFn()

	episodes, err := w.manager.EpisodesAwaitingUpload()
	if err != nil {
		return fmt.Errorf("enumerate upload backlog: %w", err)
	}
	now := w.now()
	for _, mf := range episodes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Honor backoff: a pending episode whose next-attempt time is in the
		// future is skipped this pass.
		if mf.Upload.State == uploadStatePending && mf.Upload.NextAttemptUnixNanos > now.UnixNano() {
			continue
		}
		w.processEpisode(ctx, client, mf)
	}
	return nil
}

// resolveShouldUpload applies the campaign upload.when policy. It returns
// whether the episode should upload now and a human-readable reason when it
// should not.
func (w *DataTransferWorker) resolveShouldUpload(mf data.Manifest) (bool, string) {
	when := "always"
	if name := mf.Trigger.CampaignName; name != "" {
		if c, err := w.manager.Campaign(name); err == nil {
			if c.Upload.When != "" {
				when = c.Upload.When
			}
		} else {
			// Campaign plan gone (deleted after capture). The episode was queued
			// for upload, so default to uploading it rather than stranding data.
			w.logger.Warn("data transfer worker: campaign plan not found, defaulting to always",
				zap.String("episode", mf.ID), zap.String("campaign", name), zap.Error(err))
		}
	}
	switch when {
	case "manual":
		return false, "campaign upload.when is manual"
	case "wifi":
		if w.onWiFi == nil {
			// TODO(data-platform): wire a device network-type signal so "wifi"
			// gates on actually being on an unmetered Wi-Fi link. No such signal
			// is plumbed to the worker yet, so treat "wifi" as "always".
			w.logger.Debug("data transfer worker: no network-type signal, treating upload.when=wifi as always",
				zap.String("episode", mf.ID))
			return true, ""
		}
		if !w.onWiFi() {
			return false, "campaign upload.when is wifi and device is not on Wi-Fi"
		}
		return true, ""
	default:
		return true, ""
	}
}

// uploadRate returns the campaign's bandwidth ceiling in bytes/sec for the
// episode, or 0 (unlimited) when there is no campaign or no configured cap.
func (w *DataTransferWorker) uploadRate(mf data.Manifest) int64 {
	if name := mf.Trigger.CampaignName; name != "" {
		if c, err := w.manager.Campaign(name); err == nil {
			return c.UploadMaxRateBytes()
		}
	}
	return 0
}

// processEpisode uploads a single episode. All outcomes are persisted to the
// manifest; transient failures move the episode back to "pending" with a
// backoff (or to "failed" once the retry ceiling is hit), verification failures
// move it straight to "failed", and success marks it "uploaded".
func (w *DataTransferWorker) processEpisode(ctx context.Context, client cloudpb.DataIngestServiceClient, mf data.Manifest) {
	if ok, reason := w.resolveShouldUpload(mf); !ok {
		w.logger.Debug("data transfer worker: skipping episode", zap.String("episode", mf.ID), zap.String("reason", reason))
		return
	}

	// Mark uploading (durably) before any network work so a crash mid-transfer
	// is recoverable and the quota manager knows the payload is in flight.
	if _, err := w.manager.UpdateUploadState(mf.ID, func(ws *data.WorkflowState) {
		ws.State = uploadStateUploading
	}); err != nil {
		w.logger.Warn("data transfer worker: mark uploading failed", zap.String("episode", mf.ID), zap.Error(err))
		return
	}
	w.logger.Info("data transfer worker: uploading episode", zap.String("episode", mf.ID),
		zap.Int("files", len(mf.Files)), zap.String("campaign", mf.Trigger.CampaignName))

	verifyErr, retryErr := w.uploadEpisode(ctx, client, mf)
	switch {
	case verifyErr != nil:
		// Server-side verification failed: the stored bytes do not match the
		// manifest checksum. This is local corruption; retrying re-sends the
		// same bytes and fails identically, so fail permanently.
		w.markFailed(mf.ID, fmt.Sprintf("verification failed: %v", verifyErr))
		w.logger.Error("data transfer worker: episode failed verification (marked failed, not retrying)",
			zap.String("episode", mf.ID), zap.Error(verifyErr))
	case retryErr != nil:
		if ctx.Err() != nil {
			// Shutdown mid-transfer: leave the episode "uploading" so the next
			// run resumes it. Nothing is silently dropped.
			w.logger.Info("data transfer worker: shutdown mid-upload, episode left for resume", zap.String("episode", mf.ID))
			return
		}
		w.handleRetryable(mf, retryErr)
	default:
		if _, err := w.manager.UpdateUploadState(mf.ID, func(ws *data.WorkflowState) {
			ws.State = uploadStateUploaded
			ws.LastError = ""
			ws.NextAttemptUnixNanos = 0
		}); err != nil {
			w.logger.Warn("data transfer worker: mark uploaded failed", zap.String("episode", mf.ID), zap.Error(err))
			return
		}
		w.logger.Info("data transfer worker: episode uploaded", zap.String("episode", mf.ID))
	}
}

// handleRetryable records a retryable failure: it bumps the attempt count and
// either re-queues the episode with a backoff or, once the ceiling is reached,
// marks it permanently failed.
func (w *DataTransferWorker) handleRetryable(mf data.Manifest, cause error) {
	attempts := mf.Upload.Attempts + 1
	if attempts >= w.maxAttempts {
		w.markFailed(mf.ID, fmt.Sprintf("gave up after %d attempts: %v", attempts, cause))
		w.logger.Error("data transfer worker: episode failed after retry ceiling",
			zap.String("episode", mf.ID), zap.Int("attempts", attempts), zap.Error(cause))
		return
	}
	// Exponential backoff on the retry clock, capped.
	delay := time.Second << attempts
	if delay > transferMaxBackoff || delay <= 0 {
		delay = transferMaxBackoff
	}
	next := w.now().Add(delay).UnixNano()
	if _, err := w.manager.UpdateUploadState(mf.ID, func(ws *data.WorkflowState) {
		ws.State = uploadStatePending
		ws.Attempts = attempts
		ws.LastError = cause.Error()
		ws.NextAttemptUnixNanos = next
	}); err != nil {
		w.logger.Warn("data transfer worker: requeue failed", zap.String("episode", mf.ID), zap.Error(err))
		return
	}
	w.logger.Warn("data transfer worker: episode upload failed, requeued",
		zap.String("episode", mf.ID), zap.Int("attempts", attempts), zap.Duration("backoff", delay), zap.Error(cause))
}

func (w *DataTransferWorker) markFailed(id, detail string) {
	if _, err := w.manager.UpdateUploadState(id, func(ws *data.WorkflowState) {
		ws.State = uploadStateFailed
		ws.Attempts++
		ws.LastError = detail
		ws.NextAttemptUnixNanos = 0
	}); err != nil {
		w.logger.Warn("data transfer worker: mark failed failed", zap.String("episode", id), zap.Error(err))
	}
}

// uploadEpisode runs the Begin -> chunk stream -> Commit protocol for one
// episode. It returns (verifyErr, retryErr): verifyErr is a terminal
// verification/corruption failure, retryErr is a retryable transport failure.
// At most one is non-nil; both nil means success.
func (w *DataTransferWorker) uploadEpisode(ctx context.Context, client cloudpb.DataIngestServiceClient, mf data.Manifest) (verifyErr, retryErr error) {
	manifest, err := buildEpisodeManifest(mf)
	if err != nil {
		// A manifest we cannot even serialize is corrupt local state; do not
		// spin on it.
		return err, nil
	}

	begin, err := client.BeginEpisodeUpload(ctx, &cloudpb.BeginEpisodeUploadRequest{Manifest: manifest})
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	if begin.GetState() == cloudpb.EpisodeState_EPISODE_STATE_COMPLETE {
		// The server already has a complete, verified copy (idempotent replay).
		return nil, nil
	}

	// Per-file committed offsets from the server. C3 reports 0 (restart) or the
	// full size (already stored, skip); we resume at file granularity. Mid-file
	// resume is a server-side follow-up.
	committed := make(map[string]int64, len(begin.GetFiles()))
	for _, f := range begin.GetFiles() {
		// Offsets are unsigned on the wire and signed here, because that is
		// what the file APIs use. Any value a correct server can report fits;
		// one that does not is treated as "nothing committed" so the file
		// restarts from 0 rather than wrapping to a negative offset.
		if v := f.GetCommittedOffset(); v <= math.MaxInt64 {
			committed[f.GetPath()] = int64(v)
		}
	}

	rate := w.uploadRate(mf)
	sleep := w.newSleeper(ctx)
	limiter := &byteRateLimiter{ratePerSec: float64(rate), sleep: sleep}

	stream, err := client.UploadEpisodeChunk(ctx)
	if err != nil {
		return nil, fmt.Errorf("open upload stream: %w", err)
	}

	// Drain acks concurrently so large uploads do not deadlock on flow control.
	ackErrCh := make(chan error, 1)
	go func() {
		for {
			_, rerr := stream.Recv()
			if rerr == io.EOF {
				ackErrCh <- nil
				return
			}
			if rerr != nil {
				ackErrCh <- rerr
				return
			}
		}
	}()

	sendErr := w.streamFiles(ctx, stream, mf, committed, limiter)
	// Always close the send direction so the ack reader observes EOF.
	closeErr := stream.CloseSend()
	ackErr := <-ackErrCh

	if sendErr != nil {
		return nil, fmt.Errorf("stream chunks: %w", sendErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close send: %w", closeErr)
	}
	if ackErr != nil {
		return nil, fmt.Errorf("chunk ack: %w", ackErr)
	}

	commit, err := client.CommitEpisode(ctx, &cloudpb.CommitEpisodeRequest{EpisodeId: mf.ID})
	if err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if commit.GetState() == cloudpb.EpisodeState_EPISODE_STATE_COMPLETE {
		return nil, nil
	}
	// Not complete: collect per-file verification detail. Any failed file is a
	// terminal corruption verdict.
	var details []string
	for _, v := range commit.GetFiles() {
		if !v.GetOk() {
			details = append(details, fmt.Sprintf("%s: %s (expected %s, actual %s)",
				v.GetPath(), v.GetDetail(), v.GetExpectedSha256(), v.GetActualSha256()))
		}
	}
	if len(details) > 0 {
		return errors.New(strings.Join(details, "; ")), nil
	}
	// Commit returned a non-complete state with no per-file failure detail:
	// treat as retryable so we do not permanently fail on an ambiguous verdict.
	return nil, fmt.Errorf("commit returned state %s without completion", commit.GetState())
}

// streamFiles sends every file that still needs bytes, honoring the committed
// offset and the bandwidth ceiling.
func (w *DataTransferWorker) streamFiles(ctx context.Context, stream grpc.BidiStreamingClient[cloudpb.EpisodeChunk, cloudpb.EpisodeChunkAck], mf data.Manifest, committed map[string]int64, limiter *byteRateLimiter) error {
	buf := make([]byte, transferChunkBytes)
	for _, file := range mf.Files {
		start := committed[file.Path]
		// A non-empty file whose committed offset already covers it is durably
		// stored, so skip it. A zero-length file must NOT be skipped on a zero
		// offset: streamOneFile sends it as a single empty EOF chunk so the server
		// persists the (empty) object. Skipping it here would silently drop a file
		// the manifest lists, leaving the server unaware it exists.
		if file.Size > 0 && start >= file.Size {
			continue
		}
		if err := w.streamOneFile(ctx, stream, mf.ID, file, start, buf, limiter); err != nil {
			return err
		}
	}
	return nil
}

func (w *DataTransferWorker) streamOneFile(ctx context.Context, stream grpc.BidiStreamingClient[cloudpb.EpisodeChunk, cloudpb.EpisodeChunkAck], episodeID string, file data.File, start int64, buf []byte, limiter *byteRateLimiter) error {
	f, _, err := w.manager.OpenFile(episodeID, file.Path, start)
	if err != nil {
		return fmt.Errorf("open %s: %w", file.Path, err)
	}
	defer f.Close()

	offset := start
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			limiter.take(n)
			eof := offset+int64(n) >= file.Size
			if err := stream.Send(&cloudpb.EpisodeChunk{
				EpisodeId: episodeID,
				Path:      file.Path,
				Offset:    uint64(offset),
				Data:      buf[:n],
				Eof:       eof,
			}); err != nil {
				return fmt.Errorf("send %s @%d: %w", file.Path, offset, err)
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read %s @%d: %w", file.Path, offset, readErr)
		}
	}
	// Zero-length files: send a single empty eof chunk so the server persists
	// the object (Read returned EOF immediately, no chunk sent above).
	if file.Size == 0 && start == 0 {
		if err := stream.Send(&cloudpb.EpisodeChunk{EpisodeId: episodeID, Path: file.Path, Offset: 0, Data: nil, Eof: true}); err != nil {
			return fmt.Errorf("send empty %s: %w", file.Path, err)
		}
	}
	return nil
}

// buildEpisodeManifest projects the device manifest onto the cloud
// EpisodeManifest. The full device manifest JSON rides in attributes_json for
// fidelity; the typed fields are what the catalog indexes on.
//
// The manifest carries no org or asset. The cloud reads both from the client
// certificate presented on the connection, so there is nothing here for the
// device to assert and nothing for the server to cross-check.
func buildEpisodeManifest(mf data.Manifest) (*cloudpb.EpisodeManifest, error) {
	raw, err := json.Marshal(mf)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	files := make([]*cloudpb.EpisodeFileManifest, 0, len(mf.Files))
	for _, f := range mf.Files {
		files = append(files, &cloudpb.EpisodeFileManifest{
			Path:      f.Path,
			SizeBytes: uint64(f.Size),
			Sha256:    f.SHA256,
			MediaType: f.MediaType,
			Format:    f.Format,
			SourceId:  f.SourceID,
			Role:      episodeFileRole(f.Role),
		})
	}
	var lower, upper int64
	if len(mf.UTCObservations) > 0 {
		lower = mf.UTCObservations[0].OffsetLowerNanos
		upper = mf.UTCObservations[0].OffsetUpperNanos
	}
	trigger := mf.Trigger.Reason
	if mf.Trigger.Expression != "" {
		trigger = mf.Trigger.Expression
	}
	return &cloudpb.EpisodeManifest{
		EpisodeId:            mf.ID,
		Campaign:             mf.Trigger.CampaignName,
		CampaignRevision:     mf.Trigger.CampaignRevision,
		Trigger:              trigger,
		StartedBoottimeNanos: mf.StartedEpisodeNS,
		StoppedBoottimeNanos: mf.StoppedEpisodeNS,
		StartedUnixNanos:     mf.StartedUnixNanos,
		UtcOffsetLowerNanos:  lower,
		UtcOffsetUpperNanos:  upper,
		SystemClockStatus:    mf.SystemClockStatus,
		Files:                files,
		AttributesJson:       raw,
	}, nil
}

// episodeFileRole maps the device manifest's per-file role onto the wire enum.
//
// The manifest omits the role for capture payload and capture metadata, so an
// empty role means captured. This sends CAPTURED explicitly rather than
// leaving the field at its default, so a manifest that went through this
// mapper is distinguishable on the wire from one written by an agent built
// before the field existed.
//
// Any other string maps to UNSPECIFIED, which the wire contract defines as
// captured. The enum has no "unknown" member and inventing one would force
// every reader to handle a state it cannot act on, so an unrecognised role
// degrades to the pre-existing meaning instead. That only arises when an
// older agent resumes an upload for an episode a newer build sealed; adding a
// role to data.File must add it here in the same change.
func episodeFileRole(role string) cloudpb.EpisodeFileRole {
	switch role {
	case "":
		return cloudpb.EpisodeFileRole_EPISODE_FILE_ROLE_CAPTURED
	case data.FileRoleDerived:
		return cloudpb.EpisodeFileRole_EPISODE_FILE_ROLE_DERIVED
	default:
		return cloudpb.EpisodeFileRole_EPISODE_FILE_ROLE_UNSPECIFIED
	}
}

// byteRateLimiter paces a byte stream to at most ratePerSec bytes per second by
// sleeping for the transmission time of each batch of bytes after sending it.
// A non-positive rate means unlimited. sleep is injected so tests can pace
// deterministically; production passes a context-aware sleeper.
type byteRateLimiter struct {
	ratePerSec float64
	sleep      func(time.Duration)
}

func (l *byteRateLimiter) take(n int) {
	if l == nil || l.ratePerSec <= 0 || n <= 0 || l.sleep == nil {
		return
	}
	d := time.Duration(float64(n) / l.ratePerSec * float64(time.Second))
	l.sleep(d)
}
