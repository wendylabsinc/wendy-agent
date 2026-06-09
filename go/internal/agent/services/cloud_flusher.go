package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	otelpb "github.com/wendylabsinc/wendy/go/proto/gen/otelpb"
)

// normalizeCloudHost ensures host has a port component, defaulting to :443.
func normalizeCloudHost(host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, "443")
}

const (
	cloudFlusherMaxBackoff       = 60 * time.Second
	cloudFlusherBatchSize        = 500
	cloudFlusherDefaultApp       = "device"
	cloudFlusherCollector        = "wendy-agent"
	cloudFlusherMaxEntriesPerApp = 5000 // per-RPC cap to stay under gRPC message size limits

	// PII / payload size guards applied in convertLogRecord before cloud upload.
	maxLogBodyBytes = 65536 // 64 KiB — prevents oversized payloads
	maxLabelKeyLen  = 256
	maxLabelValLen  = 1024
	maxLabels       = 64
)

// sensitiveLabelDenyList contains substrings that, when found in a label key
// (case-insensitive), indicate the value may contain credentials or PII.
// Such labels are dropped before upload to prevent accidental exposure.
var sensitiveLabelDenyList = []string{
	"password", "passwd", "secret", "token", "authorization",
	"api_key", "apikey", "private_key", "credential", "auth",
	"cookie", "session",
}

// isSensitiveLabelKey reports whether key contains a deny-listed substring.
func isSensitiveLabelKey(key string) bool {
	lower := strings.ToLower(key)
	for _, denied := range sensitiveLabelDenyList {
		if strings.Contains(lower, denied) {
			return true
		}
	}
	return false
}

// CloudFlusher reads log segments from TelemetryBuffer via ReadFromCursor,
// converts OTLP LogRecords to cloud LogEntry values, and calls WriteLogEntries.
// Metrics and traces are not uploaded in this iteration.
type CloudFlusher struct {
	logger          *zap.Logger
	buffer          *TelemetryBuffer
	provisioningSvc *ProvisioningService // nil in tests
}

// NewCloudFlusher creates a CloudFlusher for tests. The explicit org/asset ID
// parameters are preserved for compatibility with existing callers, but the
// flusher does not store them on the struct.
func NewCloudFlusher(logger *zap.Logger, buffer *TelemetryBuffer, orgID, assetID int32) *CloudFlusher {
	return &CloudFlusher{
		logger: logger,
		buffer: buffer,
	}
}

// NewCloudFlusherWithProvisioning creates a CloudFlusher that reads cloud
// credentials and IDs from the ProvisioningService at startup.
func NewCloudFlusherWithProvisioning(logger *zap.Logger, buffer *TelemetryBuffer, provisioningSvc *ProvisioningService) *CloudFlusher {
	return &CloudFlusher{
		logger:          logger,
		buffer:          buffer,
		provisioningSvc: provisioningSvc,
	}
}

// Run waits until the agent is provisioned, then continuously flushes buffered
// logs to the cloud with exponential backoff (1s → 2s → 4s … capped at 60s).
// The backoff resets on each successful flush pass. Blocks until ctx is done.
func (f *CloudFlusher) Run(ctx context.Context) {
	if f.provisioningSvc == nil {
		// Test mode: no provisioning; nothing to do.
		return
	}

	var cloudHost string
	var orgID, assetID int32

	// Poll until provisioned.
	for {
		var enrolled bool
		cloudHost, orgID, assetID, enrolled = f.provisioningSvc.ProvisioningInfo()
		if enrolled {
			break
		}
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}

	// NOTE: ProvisioningService stores the private key as []byte (zeroed on rotation).
	// Certs are re-fetched per dial so the keyData []byte copy is scoped to each
	// connection attempt and zeroed by dial on return, minimising the key-material
	// window. Crash dumps should be disabled on the device for defence-in-depth
	// (ulimit -c 0 / RLIMIT_CORE=0).

	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}

		certPEM, chainPEM, keyData := f.provisioningSvc.ProvisioningCerts()
		conn, client, err := f.dial(ctx, cloudHost, certPEM, chainPEM, keyData)
		if err != nil {
			f.logger.Warn("cloud flusher: dial failed", zap.Error(err))
			f.sleep(ctx, attempt)
			attempt++
			continue
		}

		err = f.runOnce(ctx, client, orgID, assetID)
		conn.Close()

		if err != nil {
			f.logger.Warn("cloud flusher: flush failed", zap.Error(err))
			f.sleep(ctx, attempt)
			if attempt < 6 { // 2^6 = 64s > 60s cap; further increments have no effect
				attempt++
			}
		} else {
			attempt = 0
			// Brief pause between successful passes to avoid busy-looping.
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (f *CloudFlusher) sleep(ctx context.Context, attempt int) {
	backoff := time.Duration(math.Min(
		float64(time.Second)*math.Pow(2, float64(attempt)),
		float64(cloudFlusherMaxBackoff),
	))
	select {
	case <-time.After(backoff):
	case <-ctx.Done():
	}
}

// dial establishes a TLS 1.3 gRPC connection. keyData is zeroed on return as
// best-effort protection; crypto/tls may retain additional internal copies.
func (f *CloudFlusher) dial(ctx context.Context, host, certPEM, chainPEM string, keyData []byte) (*grpc.ClientConn, cloudpb.RemoteLoggingServiceClient, error) {
	host = normalizeCloudHost(host)
	defer func() {
		for i := range keyData {
			keyData[i] = 0
		}
	}()
	// Build client cert PEM bundle: leaf cert + intermediate chain so that
	// servers can verify the full chain without trusting the leaf directly.
	certBundle := []byte(certPEM)
	if chainPEM != "" {
		certBundle = append(certBundle, '\n')
		certBundle = append(certBundle, []byte(chainPEM)...)
	}
	cert, err := tls.X509KeyPair(certBundle, keyData)
	if err != nil {
		return nil, nil, fmt.Errorf("cloud flusher: parse key pair: %w", err)
	}

	caPool, err := x509.SystemCertPool()
	if err != nil {
		caPool = x509.NewCertPool()
	}
	if chainPEM != "" {
		caPool.AppendCertsFromPEM([]byte(chainPEM))
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
	}

	conn, err := grpc.NewClient(host, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, nil, err
	}
	return conn, cloudpb.NewRemoteLoggingServiceClient(conn), nil
}

// runOnce performs a single read-convert-upload pass. It does not advance the
// cursor if WriteLogEntries returns an error (ensuring retry safety).
func (f *CloudFlusher) runOnce(ctx context.Context, client cloudpb.RemoteLoggingServiceClient, orgID, assetID int32) error {
	if f.buffer == nil {
		return nil
	}
	// cursor.json is HMAC-SHA256 integrity-protected via a device-local key
	// stored in cursor.key. Any tampering or corruption is detected by
	// LoadCursor, which resets the offset to zero rather than trusting a
	// manipulated value.
	cursor := f.buffer.LoadCursor(SignalLogs)
	msgs, next, err := f.buffer.ReadFromCursor(SignalLogs, cursor, cloudFlusherBatchSize)
	if err != nil {
		return fmt.Errorf("cloud flusher: read from cursor: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	// Group entries by app ID (service.name resource attribute).
	appEntries := f.groupByApp(msgs)

	// Sort app IDs for deterministic iteration order so that partial-failure
	// retries always re-send apps in the same sequence.
	appIDs := make([]string, 0, len(appEntries))
	for appID := range appEntries {
		appIDs = append(appIDs, appID)
	}
	sort.Strings(appIDs)

	// Each app's entries are sent in one or more RPCs, chunked to at most
	// cloudFlusherMaxEntriesPerApp entries to stay within gRPC message size limits.
	// On any failure, the cursor is NOT advanced and the entire batch is retried —
	// already-uploaded entries will be re-sent. This is intentional at-least-once
	// delivery; the cloud accepts duplicates.
	collectorStr := cloudFlusherCollector
	var sentAny bool
	for _, appID := range appIDs {
		allEntries := appEntries[appID]
		for start := 0; start < len(allEntries); start += cloudFlusherMaxEntriesPerApp {
			end := start + cloudFlusherMaxEntriesPerApp
			if end > len(allEntries) {
				end = len(allEntries)
			}
			req := &cloudpb.WriteLogEntriesRequest{
				OrganizationId: orgID,
				AssetId:        assetID,
				AppId:          appID,
				Collector:      &collectorStr,
				Entries:        allEntries[start:end],
			}
			if _, err := client.WriteLogEntries(ctx, req); err != nil {
				if sentAny {
					// Warn operators that already-uploaded entries will be re-sent on retry.
					f.logger.Warn("cloud flusher: partial batch failure; prior entries will be re-sent on retry",
						zap.String("failed_app", appID),
					)
				}
				// Do NOT advance cursor; caller will retry.
				return fmt.Errorf("cloud flusher: WriteLogEntries (app=%s): %w", appID, err)
			}
			sentAny = true
		}
	}

	// All batches succeeded — advance the cursor.
	if err := f.buffer.SaveCursor(SignalLogs, next); err != nil {
		f.logger.Warn("cloud flusher: failed to save cursor", zap.Error(err))
		return fmt.Errorf("saving cursor: %w", err)
	}
	return nil
}

// groupByApp converts OTLP log messages to cloud LogEntry values grouped by
// the service.name resource attribute. Entries without a service.name are
// grouped under "device".
func (f *CloudFlusher) groupByApp(msgs []proto.Message) map[string][]*cloudpb.LogEntry {
	result := make(map[string][]*cloudpb.LogEntry)

	for _, msg := range msgs {
		req, ok := msg.(*otelpb.ExportLogsServiceRequest)
		if !ok {
			continue
		}
		for _, rl := range req.GetResourceLogs() {
			appID := cloudFlusherDefaultApp
			if res := rl.GetResource(); res != nil {
				for _, kv := range res.GetAttributes() {
					if kv.GetKey() == "service.name" {
						if s := kv.GetValue().GetStringValue(); s != "" {
							appID = s
						}
						break
					}
				}
			}

			for _, sl := range rl.GetScopeLogs() {
				for _, lr := range sl.GetLogRecords() {
					entry := convertLogRecord(lr)
					result[appID] = append(result[appID], entry)
				}
			}
		}
	}
	return result
}

// convertLogRecord converts a single OTLP LogRecord to a cloud LogEntry.
func convertLogRecord(lr *otelpb.LogRecord) *cloudpb.LogEntry {
	entry := &cloudpb.LogEntry{
		Severity: otelSeverityToCloud(lr.GetSeverityNumber()),
	}

	// Timestamp.
	if ns := lr.GetTimeUnixNano(); ns != 0 {
		entry.Timestamp = timestamppb.New(time.Unix(0, int64(ns)))
	}
	if ns := lr.GetObservedTimeUnixNano(); ns != 0 {
		entry.ObservedAt = timestamppb.New(time.Unix(0, int64(ns)))
	}

	// Trace/span IDs as hex strings.
	if traceID := lr.GetTraceId(); len(traceID) == 16 {
		entry.TraceId = hex.EncodeToString(traceID)
	}
	if spanID := lr.GetSpanId(); len(spanID) == 8 {
		entry.SpanId = hex.EncodeToString(spanID)
	}

	// Body → text payload. Truncated to maxLogBodyBytes to prevent oversized
	// entries from reaching the cloud and to guard against data-exfiltration
	// via abnormally large log bodies.
	if body := lr.GetBody(); body != nil {
		text := otelAnyValueString(body)
		if len(text) > maxLogBodyBytes {
			text = text[:maxLogBodyBytes]
		}
		entry.Payload = &cloudpb.LogEntry_TextPayload{
			TextPayload: text,
		}
	}

	// Copy log-record attributes into labels, applying PII / size guards:
	//   • keys matching the credential/PII deny-list are dropped entirely
	//   • keys and values are truncated to avoid excessive data transfer
	//   • total label count is capped at maxLabels
	if attrs := lr.GetAttributes(); len(attrs) > 0 {
		labels := make(map[string]string, min(len(attrs), maxLabels))
		for _, kv := range attrs {
			if len(labels) >= maxLabels {
				break
			}
			k := kv.GetKey()
			if isSensitiveLabelKey(k) {
				continue
			}
			if len(k) > maxLabelKeyLen {
				k = k[:maxLabelKeyLen]
			}
			v := otelAnyValueString(kv.GetValue())
			if len(v) > maxLabelValLen {
				v = v[:maxLabelValLen]
			}
			labels[k] = v
		}
		entry.Labels = labels
	}

	return entry
}

// otelAnyValueString converts an OTLP AnyValue to a human-readable string.
func otelAnyValueString(v *otelpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.GetValue().(type) {
	case *otelpb.AnyValue_StringValue:
		return val.StringValue
	case *otelpb.AnyValue_BoolValue:
		if val.BoolValue {
			return "true"
		}
		return "false"
	case *otelpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", val.IntValue)
	case *otelpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", val.DoubleValue)
	case *otelpb.AnyValue_BytesValue:
		return hex.EncodeToString(val.BytesValue)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// otelSeverityToCloud maps an OTLP SeverityNumber to a cloud LogSeverity.
func otelSeverityToCloud(sev otelpb.SeverityNumber) cloudpb.LogSeverity {
	switch {
	case sev == otelpb.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED:
		return cloudpb.LogSeverity_LOG_SEVERITY_UNSPECIFIED
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_TRACE4:
		return cloudpb.LogSeverity_LOG_SEVERITY_DEBUG
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_DEBUG4:
		return cloudpb.LogSeverity_LOG_SEVERITY_DEBUG
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_INFO && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_INFO4:
		return cloudpb.LogSeverity_LOG_SEVERITY_INFO
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_WARN && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_WARN4:
		return cloudpb.LogSeverity_LOG_SEVERITY_WARNING
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_ERROR4:
		return cloudpb.LogSeverity_LOG_SEVERITY_ERROR
	case sev >= otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL && sev <= otelpb.SeverityNumber_SEVERITY_NUMBER_FATAL4:
		return cloudpb.LogSeverity_LOG_SEVERITY_CRITICAL
	default:
		return cloudpb.LogSeverity_LOG_SEVERITY_UNSPECIFIED
	}
}
