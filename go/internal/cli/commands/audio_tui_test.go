package commands

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type fakeAudioTUIHandler struct {
	defaultDevice *agentpbv2.AudioDevice
	volumeDevice  *agentpbv2.AudioDevice
	volume        uint32
}

func (h *fakeAudioTUIHandler) SetDefault(device *agentpbv2.AudioDevice) tea.Cmd {
	h.defaultDevice = device
	return func() tea.Msg {
		return audioOpResultMsg{
			action: audioActionSetDefault, deviceID: device.GetDeviceId(), deviceType: device.GetType(),
		}
	}
}

func (h *fakeAudioTUIHandler) SetVolume(device *agentpbv2.AudioDevice, volume uint32) tea.Cmd {
	h.volumeDevice = device
	h.volume = volume
	return func() tea.Msg {
		return audioOpResultMsg{action: audioActionSetVolume, deviceID: device.GetDeviceId(), volume: volume}
	}
}

func audioTUITestDevices() []*agentpbv2.AudioDevice {
	volume := uint32(40)
	return []*agentpbv2.AudioDevice{
		{DeviceId: 513, Name: "hw:2,0", Description: "USB microphone", Type: agentpbv2.AudioDeviceType_AUDIO_DEVICE_TYPE_INPUT},
		{DeviceId: 513, Name: "hw:2,0", Description: "USB speaker", Type: agentpbv2.AudioDeviceType_AUDIO_DEVICE_TYPE_OUTPUT, VolumePercent: &volume},
	}
}

func TestAudioTUIRightArrowRaisesOutputVolume(t *testing.T) {
	handler := &fakeAudioTUIHandler{}
	model := newAudioTUIModel(audioTUITestDevices(), handler)
	model.table.SetCursor(1)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(audioTUIModel)
	if cmd == nil {
		t.Fatal("right arrow did not dispatch a volume command")
	}
	if handler.volumeDevice == nil || handler.volumeDevice.GetDeviceId() != 513 || handler.volume != 45 {
		t.Fatalf("volume request = device %v volume %d; want device 513 volume 45", handler.volumeDevice, handler.volume)
	}

	updated, _ = model.Update(cmd())
	model = updated.(audioTUIModel)
	if got := model.devices[1].GetVolumePercent(); got != 45 {
		t.Fatalf("updated volume = %d, want 45", got)
	}
	if !strings.Contains(model.View(), "45%") {
		t.Fatal("view does not show the updated volume")
	}
}

func TestAudioTUILeftArrowClampsAtZero(t *testing.T) {
	devices := audioTUITestDevices()
	volume := uint32(3)
	devices[1].VolumePercent = &volume
	handler := &fakeAudioTUIHandler{}
	model := newAudioTUIModel(devices, handler)
	model.table.SetCursor(1)

	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if cmd == nil {
		t.Fatal("left arrow did not dispatch a volume command")
	}
	if handler.volume != 0 {
		t.Fatalf("volume request = %d, want 0", handler.volume)
	}
}

func TestAudioTUIEnterSetsDefault(t *testing.T) {
	handler := &fakeAudioTUIHandler{}
	model := newAudioTUIModel(audioTUITestDevices(), handler)
	model.table.SetCursor(1)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(audioTUIModel)
	if cmd == nil || handler.defaultDevice == nil || handler.defaultDevice.GetDeviceId() != 513 {
		t.Fatal("enter did not dispatch set-default for the selected device")
	}

	updated, _ = model.Update(cmd())
	model = updated.(audioTUIModel)
	if !model.devices[1].GetIsDefault() {
		t.Fatal("selected output was not marked as default after success")
	}
	if model.devices[0].GetIsDefault() {
		t.Fatal("setting the output default changed the input default")
	}
}

func TestAudioTUIRejectsVolumeOnInputOrUnsupportedOutput(t *testing.T) {
	handler := &fakeAudioTUIHandler{}
	model := newAudioTUIModel(audioTUITestDevices(), handler)

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(audioTUIModel)
	if cmd != nil || handler.volumeDevice != nil || !model.isError {
		t.Fatal("input volume change should be rejected locally")
	}

	model.table.SetCursor(1)
	model.devices[1].VolumePercent = nil
	model.refreshRows()
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	model = updated.(audioTUIModel)
	if cmd != nil || handler.volumeDevice != nil || !strings.Contains(model.flash, "update the device agent") {
		t.Fatalf("unsupported output result: cmd=%v device=%v flash=%q", cmd, handler.volumeDevice, model.flash)
	}
}
