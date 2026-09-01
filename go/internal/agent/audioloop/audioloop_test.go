package audioloop

import "testing"

func TestAllocateStableAndUnique(t *testing.T) {
	m := NewManager(nil)
	a, err := m.Allocate(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := m.Allocate(1, 10); again != a {
		t.Fatalf("expected stable subdevice, got %d then %d", a, again)
	}
	b, _ := m.Allocate(2, 10)
	if b == a {
		t.Fatalf("different (source,channel) must get distinct subdevices, both %d", a)
	}
}

func TestAllocateExhaustion(t *testing.T) {
	m := NewManager(nil)
	for i := 0; i < MaxSubdevices; i++ {
		if _, err := m.Allocate(int32(i), 0); err != nil {
			t.Fatalf("alloc %d: %v", i, err)
		}
	}
	if _, err := m.Allocate(99, 0); err == nil {
		t.Fatal("expected exhaustion error")
	}
}

func TestAplayArgs(t *testing.T) {
	got := aplayArgs("hw:Loopback,0,3", PCMFormat{SampleRate: 48000, Channels: 2})
	want := []string{"-D", "hw:Loopback,0,3", "-f", "S16_LE", "-r", "48000", "-c", "2", "-t", "raw", "--buffer-time=200000", "--period-time=25000", "-q", "-"}
	if len(got) != len(want) {
		t.Fatalf("args %v != %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d: %q != %q", i, got[i], want[i])
		}
	}
}
