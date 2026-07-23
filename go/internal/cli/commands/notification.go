package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

const notificationSendTimeout = 15 * time.Second

type notificationSendOptions struct {
	cloudGRPC    string
	organization int32
	user         string
	team         int32
	role         string
	title        string
	body         string
	severity     string
	deepLink     string
	sourceID     string
	metadataJSON string
}

func newNotificationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notification",
		Short: "Manage Wendy Notifications",
	}
	cmd.AddCommand(newNotificationSendCmd())
	return cmd
}

func newNotificationSendCmd() *cobra.Command {
	var options notificationSendOptions
	cmd := &cobra.Command{
		Use:     "send",
		Aliases: []string{"create"},
		Short:   "Send a Notification to a user, organization team, or role",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			auth, err := pickAuthEntry(options.cloudGRPC)
			if err != nil {
				return err
			}
			if auth.APIKey == "" {
				return fmt.Errorf("this command requires JWT/PAT authentication; re-run 'wendy cloud login'")
			}
			if options.organization == 0 {
				if len(auth.Certificates) == 0 || auth.Certificates[0].OrganizationID <= 0 {
					return fmt.Errorf("organization is required; pass --organization")
				}
				options.organization = int32(auth.Certificates[0].OrganizationID)
			}

			request, err := buildNotificationSendRequest(options)
			if err != nil {
				return err
			}
			conn, err := dialCloudGRPC(auth)
			if err != nil {
				return err
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), notificationSendTimeout)
			defer cancel()
			ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+auth.APIKey)
			response, err := cloudpb.NewNotificationServiceClient(conn).CreateNotificationV2(ctx, request)
			if err != nil {
				return fmt.Errorf("sending notification: %w", err)
			}
			if jsonOutput {
				output, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(response)
				if err != nil {
					return fmt.Errorf("encoding response: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(output))
				return nil
			}
			if response.GetDuplicate() {
				fmt.Fprintf(cmd.OutOrStdout(), "Notification already accepted (%d recipient(s)).\n", response.GetRecipientCount())
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Notification sent to %d recipient(s).\n", response.GetRecipientCount())
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&options.cloudGRPC, "cloud-grpc", "", "Cloud gRPC endpoint (optional when a default session is selected)")
	flags.Int32VarP(&options.organization, "organization", "o", 0, "Organization ID (defaults to the selected session's organization)")
	flags.StringVar(&options.user, "user", "", "Recipient user ID")
	flags.Int32Var(&options.team, "team", 0, "Recipient organization team ID")
	flags.StringVar(&options.role, "role", "", "Recipient organization role: owner, admin, billing_manager, member, or viewer")
	flags.StringVar(&options.title, "title", "", "Notification title")
	flags.StringVar(&options.body, "body", "", "Notification body")
	flags.StringVar(&options.severity, "severity", "info", "Severity: info, warning, error, or critical")
	flags.StringVar(&options.deepLink, "deep-link", "", "Absolute wendy:// destination")
	flags.StringVar(&options.sourceID, "source-id", "", "Idempotency ID (generated when omitted)")
	flags.StringVar(&options.metadataJSON, "metadata", "", "Optional JSON object metadata")
	return cmd
}

func buildNotificationSendRequest(options notificationSendOptions) (*cloudpb.CreateNotificationV2Request, error) {
	if options.organization <= 0 {
		return nil, fmt.Errorf("organization must be greater than zero")
	}
	if strings.TrimSpace(options.title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if strings.TrimSpace(options.body) == "" {
		return nil, fmt.Errorf("body is required")
	}
	if !strings.HasPrefix(options.deepLink, "wendy://") {
		return nil, fmt.Errorf("deep-link must be an absolute wendy:// URI")
	}

	audience, err := notificationAudience(options)
	if err != nil {
		return nil, err
	}
	severity, err := notificationSeverity(options.severity)
	if err != nil {
		return nil, err
	}
	sourceID := strings.TrimSpace(options.sourceID)
	if sourceID == "" {
		sourceID, err = newNotificationSourceID()
		if err != nil {
			return nil, err
		}
	}

	request := &cloudpb.CreateNotificationV2Request{
		OrganizationId: &options.organization,
		Audience:       audience,
		Title:          options.title,
		Body:           options.body,
		Severity:       severity,
		DeepLink:       options.deepLink,
		SourceId:       sourceID,
	}
	if strings.TrimSpace(options.metadataJSON) != "" {
		var value map[string]any
		if err := json.Unmarshal([]byte(options.metadataJSON), &value); err != nil {
			return nil, fmt.Errorf("metadata must be a JSON object: %w", err)
		}
		if value == nil {
			return nil, fmt.Errorf("metadata must be a JSON object")
		}
		request.Metadata, err = structpb.NewStruct(value)
		if err != nil {
			return nil, fmt.Errorf("metadata must contain JSON-compatible values: %w", err)
		}
	}
	return request, nil
}

func notificationAudience(options notificationSendOptions) (*cloudpb.NotificationAudience, error) {
	user := strings.TrimSpace(options.user)
	roleName := strings.TrimSpace(options.role)
	count := 0
	if user != "" {
		count++
	}
	if options.team > 0 {
		count++
	}
	if roleName != "" {
		count++
	}
	if count != 1 {
		return nil, fmt.Errorf("exactly one of --user, --team, or --role is required")
	}
	switch {
	case user != "":
		return &cloudpb.NotificationAudience{Audience: &cloudpb.NotificationAudience_UserId{UserId: user}}, nil
	case options.team > 0:
		return &cloudpb.NotificationAudience{Audience: &cloudpb.NotificationAudience_OrgTeamId{OrgTeamId: options.team}}, nil
	default:
		role, err := notificationRole(roleName)
		if err != nil {
			return nil, err
		}
		return &cloudpb.NotificationAudience{Audience: &cloudpb.NotificationAudience_OrganizationRole{OrganizationRole: role}}, nil
	}
}

func notificationRole(value string) (cloudpb.OrganizationRole, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "owner":
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_OWNER, nil
	case "admin":
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_ADMIN, nil
	case "billing_manager", "billing-manager":
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_BILLING_MANAGER, nil
	case "member":
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_MEMBER, nil
	case "viewer":
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_VIEWER, nil
	default:
		return cloudpb.OrganizationRole_ORGANIZATION_ROLE_UNSPECIFIED, fmt.Errorf("unsupported role %q", value)
	}
}

func notificationSeverity(value string) (cloudpb.NotificationSeverity, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "info":
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_INFO, nil
	case "warning":
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_WARNING, nil
	case "error":
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_ERROR, nil
	case "critical":
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_CRITICAL, nil
	default:
		return cloudpb.NotificationSeverity_NOTIFICATION_SEVERITY_UNSPECIFIED, fmt.Errorf("unsupported severity %q", value)
	}
}

func newNotificationSourceID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generating source ID: %w", err)
	}
	return "wendy-cli-" + hex.EncodeToString(bytes[:]), nil
}
