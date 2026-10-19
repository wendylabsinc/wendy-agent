package ble

import (
	"fmt"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/ble/agentinfo"
	"github.com/wendylabsinc/wendy/go/internal/shared/ble/central"
)

// ReadAgentInfoAt reads public discovery metadata and closes the temporary
// connection before the caller opens the authenticated command session.
func ReadAgentInfoAt(address string, timeout time.Duration) (*agentinfo.Info, error) {
	conn, err := central.Connect(address, central.TimeoutSeconds(timeout))
	if err != nil {
		return nil, fmt.Errorf("connecting for agent BLE metadata: %w", err)
	}
	defer conn.Close()
	if err := conn.DiscoverServices(central.TimeoutSeconds(timeout)); err != nil {
		return nil, err
	}
	if !conn.HasService(agentinfo.ServiceUUID) {
		return nil, fmt.Errorf("agent BLE metadata service is unavailable")
	}
	data, err := conn.ReadCharacteristic(agentinfo.ServiceUUID, agentinfo.CharacteristicUUID)
	if err != nil {
		return nil, fmt.Errorf("reading agent BLE metadata: %w", err)
	}
	return agentinfo.Decode(data)
}
