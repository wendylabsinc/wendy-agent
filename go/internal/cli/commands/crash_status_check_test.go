package commands

import (
	"testing"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

func TestDueCrashStatusCheck(t *testing.T) {
	if dueCrashStatusCheck(&config.Config{}) {
		t.Error("no subscriptions → not due")
	}
	recent := &config.Config{CrashReport: &config.CrashReportConfig{
		SubscribedReports:    []string{"WDY-ABC123"},
		LastCrashStatusCheck: time.Now().UTC().Format(time.RFC3339),
	}}
	if dueCrashStatusCheck(recent) {
		t.Error("checked just now → not due")
	}
	stale := &config.Config{CrashReport: &config.CrashReportConfig{
		SubscribedReports:    []string{"WDY-ABC123"},
		LastCrashStatusCheck: time.Now().UTC().Add(-7 * time.Hour).Format(time.RFC3339),
	}}
	if !dueCrashStatusCheck(stale) {
		t.Error("stale + subscriptions → due")
	}
}
