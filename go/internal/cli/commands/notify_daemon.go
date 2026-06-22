package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
)

// notificationSubscriber is the narrow interface the daemon needs from the cloud client.
type notificationSubscriber interface {
	SubscribeNotifications(ctx context.Context, in *cloudpb.SubscribeNotificationsRequest, opts ...grpc.CallOption) (cloudpb.NotificationService_SubscribeNotificationsClient, error)
}

// notifyState maps cloud gRPC endpoints to per-endpoint state.
type notifyState map[string]notifyEndpointState

type notifyEndpointState struct {
	LastSeenID int32 `json:"lastSeenId"`
}

// notifyDaemonDeps holds injectable dependencies for the daemon loop.
type notifyDaemonDeps struct {
	newClient    func(*config.AuthConfig) (notificationSubscriber, func(), error)
	sendNotif    func(title, body string) error
	sleep        func(ctx context.Context, d time.Duration) error
	refreshCerts func(ctx context.Context, auth *config.AuthConfig) error
	loadState    func() (notifyState, error)
	saveState    func(notifyState) error
}

func defaultNotifyDaemonDeps() notifyDaemonDeps {
	return notifyDaemonDeps{
		newClient: func(auth *config.AuthConfig) (notificationSubscriber, func(), error) {
			conn, err := dialCloudGRPC(auth)
			if err != nil {
				return nil, nil, err
			}
			return cloudpb.NewNotificationServiceClient(conn), func() { conn.Close() }, nil
		},
		sendNotif: sendOSNotification,
		sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		},
		refreshCerts: func(ctx context.Context, auth *config.AuthConfig) error {
			return refreshCertsForAuth(ctx, auth)
		},
		loadState: loadNotifyState,
		saveState: saveNotifyState,
	}
}

// runNotifyDaemon streams notifications from Wendy Cloud and fires native OS
// notifications. It reconnects with exponential backoff until ctx is cancelled.
func runNotifyDaemon(ctx context.Context, auth *config.AuthConfig, deps notifyDaemonDeps) error {
	state, err := deps.loadState()
	if err != nil {
		log.Printf("wendy notify: loading state: %v (starting fresh)", err)
		state = make(notifyState)
	}

	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := runNotifyStream(ctx, auth, state, deps)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil && isCertRefreshableError(err) {
			log.Printf("wendy notify: cert error (%v), attempting refresh", err)
			if rerr := deps.refreshCerts(ctx, auth); rerr != nil {
				log.Printf("wendy notify: cert refresh failed: %v", rerr)
			}
		} else if err != nil && err != io.EOF {
			log.Printf("wendy notify: stream error: %v", err)
		}

		if err := deps.sleep(ctx, backoff); err != nil {
			return ctx.Err()
		}
		if backoff < 5*time.Minute {
			backoff *= 2
		}
	}
}

func runNotifyStream(ctx context.Context, auth *config.AuthConfig, state notifyState, deps notifyDaemonDeps) error {
	client, closeConn, err := deps.newClient(auth)
	if err != nil {
		return fmt.Errorf("connecting to cloud: %w", err)
	}
	defer closeConn()

	ep := auth.CloudGRPC
	epState := state[ep]

	req := &cloudpb.SubscribeNotificationsRequest{
		OrganizationId: int32(auth.Certificates[0].OrganizationID),
		UserId:         auth.Certificates[0].UserID,
	}
	if epState.LastSeenID > 0 {
		id := epState.LastSeenID
		req.AfterId = &id
	}

	stream, err := client.SubscribeNotifications(ctx, req)
	if err != nil {
		return fmt.Errorf("subscribing: %w", err)
	}

	for {
		n, err := stream.Recv()
		if err != nil {
			return err
		}

		title := severityTitle(n.Severity)
		if nerr := deps.sendNotif(title, n.Body); nerr != nil {
			log.Printf("wendy notify: sending OS notification: %v", nerr)
		}

		state[ep] = notifyEndpointState{LastSeenID: n.Id}
		if serr := deps.saveState(state); serr != nil {
			log.Printf("wendy notify: saving state: %v", serr)
		}
	}
}

func severityTitle(sev cloudpb.NotificationSeverity) string {
	switch sev {
	case cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING:
		return "Wendy — Warning"
	case cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR:
		return "Wendy — Error"
	case cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL:
		return "Wendy — Critical"
	default:
		return "Wendy"
	}
}

func notifyStatePath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "notify-state.json"), nil
}

func loadNotifyState() (notifyState, error) {
	path, err := notifyStatePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return make(notifyState), nil
	}
	if err != nil {
		return nil, err
	}
	var s notifyState
	if err := json.Unmarshal(data, &s); err != nil {
		return make(notifyState), nil
	}
	return s, nil
}

func saveNotifyState(s notifyState) error {
	path, err := notifyStatePath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
