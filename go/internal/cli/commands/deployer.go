package commands

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
)

// deployerResolver maps a recorded deployed_by principal (the mTLS cert CN, e.g.
// "wendy/user/<uid>") to a human-friendly display string:
//
//   - "you" when the principal is the currently logged-in user (no cloud call),
//   - the user's email (falling back to name) resolved via the cloud
//     UserService for everyone else, and
//   - the raw principal as a graceful fallback when offline, unauthenticated, or
//     the lookup fails.
//
// Results are cached per process; lookups are safe for concurrent use (the
// dashboard resolves off its UI thread). A resolver should be Close()d when done.
type deployerResolver struct {
	mu      sync.Mutex
	selfUID string
	auth    *config.AuthConfig
	cache   map[string]string // uid -> display
	conn    *grpc.ClientConn
	client  cloudpb.UserServiceClient
	dialErr bool // cloud dial already failed; stop trying
}

func newDeployerResolver() *deployerResolver {
	r := &deployerResolver{cache: map[string]string{}}
	if cfg, err := config.Load(); err == nil {
		for i := range cfg.Auth {
			if len(cfg.Auth[i].Certificates) > 0 {
				r.auth = &cfg.Auth[i]
				r.selfUID = cfg.Auth[i].Certificates[0].UserID
				break
			}
		}
	}
	return r
}

// principalUID extracts the bare user id from a recorded deployed_by value.
// The recorded form is "wendy/user/<uid>". Returns ok=false for non-user
// principals (e.g. "wendy/asset/...") so the caller shows the principal as-is.
// A trailing " (org N)" is tolerated for any labels recorded by an earlier
// build that included it.
func principalUID(by string) (string, bool) {
	s := by
	if i := strings.Index(s, " ("); i >= 0 { // tolerate a legacy " (org N)" suffix
		s = s[:i]
	}
	const prefix = "wendy/user/"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}
	uid := strings.TrimSpace(strings.TrimPrefix(s, prefix))
	return uid, uid != ""
}

// Resolve returns the display string for a recorded deployed_by principal. An
// empty principal returns "" (callers render an em-dash). It never errors: any
// failure falls back to the raw principal.
func (r *deployerResolver) Resolve(ctx context.Context, by string) string {
	if by == "" {
		return ""
	}
	uid, ok := principalUID(by)
	if !ok {
		return by
	}
	if r.selfUID != "" && uid == r.selfUID {
		return "you"
	}

	r.mu.Lock()
	if v, hit := r.cache[uid]; hit {
		r.mu.Unlock()
		return v
	}
	r.mu.Unlock()

	display := by // fallback to the raw principal
	if email := r.lookupEmail(ctx, uid); email != "" {
		display = email
	}

	r.mu.Lock()
	r.cache[uid] = display
	r.mu.Unlock()
	return display
}

// lookupEmail calls the cloud UserService for uid, returning the email (or name)
// or "" on any failure. The cloud connection is dialed lazily and reused.
func (r *deployerResolver) lookupEmail(ctx context.Context, uid string) string {
	r.mu.Lock()
	if r.auth == nil || r.dialErr {
		r.mu.Unlock()
		return ""
	}
	if r.client == nil {
		conn, err := dialCloudGRPC(r.auth)
		if err != nil {
			r.dialErr = true
			r.mu.Unlock()
			return ""
		}
		r.conn = conn
		r.client = cloudpb.NewUserServiceClient(conn)
	}
	client, auth := r.client, r.auth
	r.mu.Unlock()

	cctx, cancel := context.WithTimeout(cloudContext(ctx, auth), 3*time.Second)
	defer cancel()
	user, err := client.GetUser(cctx, &cloudpb.GetUserRequest{Id: uid})
	if err != nil {
		return ""
	}
	if email := user.GetEmail(); email != "" {
		return email
	}
	return user.GetName()
}

func (r *deployerResolver) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		_ = r.conn.Close()
		r.conn = nil
	}
}
