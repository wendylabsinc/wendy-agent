#!/usr/bin/env python3
"""
Microphone Test Utility
Tests audio input devices and helps diagnose microphone issues
"""

import sounddevice as sd
import numpy as np
import sys
import time
import argparse

def list_audio_devices():
    """List all available audio devices"""
    print("\n" + "="*60)
    print("AVAILABLE AUDIO DEVICES")
    print("="*60)

    devices = sd.query_devices()
    input_devices = []

    for i, device in enumerate(devices):
        if device['max_input_channels'] > 0:
            input_devices.append(i)
            default_marker = " [DEFAULT]" if i == sd.default.device[0] else ""
            print(f"\nDevice #{i}{default_marker}:")
            print(f"  Name: {device['name']}")
            print(f"  Channels: {device['max_input_channels']}")
            print(f"  Sample Rate: {device['default_samplerate']} Hz")
            print(f"  Host API: {device['hostapi']}")

    if not input_devices:
        print("\n*** NO INPUT DEVICES FOUND ***")
        print("Please check your microphone connection.")

    return input_devices

def test_audio_input(device_index=None, duration=5, sample_rate=16000):
    """
    Test audio input from specified device

    Args:
        device_index: Device index to test (None for default)
        duration: Test duration in seconds
        sample_rate: Sample rate in Hz
    """
    device_name = "default" if device_index is None else f"#{device_index}"

    print(f"\n" + "="*60)
    print(f"TESTING AUDIO INPUT - Device {device_name}")
    print("="*60)
    print(f"Duration: {duration} seconds")
    print(f"Sample Rate: {sample_rate} Hz")
    print("\nSpeak into the microphone...")
    print("Volume level will be displayed in real-time")
    print("-"*60)

    try:
        # Record audio
        recording = sd.rec(
            int(duration * sample_rate),
            samplerate=sample_rate,
            channels=1,
            device=device_index,
            dtype='float32'
        )

        # Display volume levels in real-time
        start_time = time.time()
        max_volume = 0
        min_volume = 1.0
        samples_detected = 0

        while time.time() - start_time < duration:
            # Wait for a small chunk to be recorded
            time.sleep(0.1)
            elapsed = time.time() - start_time
            samples_available = int(elapsed * sample_rate)

            if samples_available > 0:
                # Calculate RMS of recorded samples so far
                chunk = recording[:samples_available]
                rms = np.sqrt(np.mean(chunk**2))

                # Update statistics
                if rms > max_volume:
                    max_volume = rms
                if rms < min_volume and rms > 0:
                    min_volume = rms
                if rms > 0.01:  # Threshold for detecting sound
                    samples_detected += 1

                # Create visual bar
                bar_length = int(rms * 100)
                bar = '#' * min(bar_length, 50)

                # Display
                remaining = duration - elapsed
                print(f"\r[{elapsed:4.1f}s] Volume: [{bar:<50}] {rms:.4f} | Remaining: {remaining:3.1f}s",
                      end='', flush=True)

        # Wait for recording to complete
        sd.wait()

        # Analyze results
        print(f"\n" + "-"*60)
        print("TEST RESULTS:")
        print(f"  Recording completed: YES")
        print(f"  Max volume detected: {max_volume:.4f}")
        print(f"  Min volume detected: {min_volume:.4f}")

        # Check if audio was detected
        overall_rms = np.sqrt(np.mean(recording**2))
        if overall_rms < 0.001:
            print("\n*** WARNING: No audio detected! ***")
            print("Possible issues:")
            print("  - Microphone may be muted")
            print("  - Wrong device selected")
            print("  - Microphone not properly connected")
            print("  - Insufficient permissions")
        elif overall_rms < 0.01:
            print("\n*** WARNING: Very low audio level detected ***")
            print("Possible issues:")
            print("  - Microphone volume too low")
            print("  - Microphone too far away")
            print("  - Background noise cancellation too aggressive")
        else:
            print(f"\n✓ Audio successfully detected!")
            print(f"  Average volume: {overall_rms:.4f}")

            # Frequency analysis
            fft = np.fft.fft(recording.flatten())
            freqs = np.fft.fftfreq(len(fft), 1/sample_rate)
            magnitude = np.abs(fft)

            # Find dominant frequency
            positive_freqs = freqs[:len(freqs)//2]
            positive_magnitude = magnitude[:len(magnitude)//2]
            dominant_freq_idx = np.argmax(positive_magnitude[1:]) + 1  # Skip DC component
            dominant_freq = positive_freqs[dominant_freq_idx]

            print(f"  Dominant frequency: {dominant_freq:.1f} Hz")

            # Estimate if it's voice
            if 85 <= dominant_freq <= 3000:
                print("  Frequency range suggests human voice detected")

        return True

    except Exception as e:
        print(f"\n*** ERROR during recording: {e} ***")
        print("\nPossible solutions:")
        print("  - Check if the device is properly connected")
        print("  - Verify device permissions")
        print("  - Try a different device index")
        print("  - Ensure no other application is using the microphone")
        return False

def continuous_monitor(device_index=None, sample_rate=16000):
    """
    Continuously monitor audio levels

    Args:
        device_index: Device index to monitor (None for default)
        sample_rate: Sample rate in Hz
    """
    device_name = "default" if device_index is None else f"#{device_index}"

    print(f"\n" + "="*60)
    print(f"CONTINUOUS AUDIO MONITORING - Device {device_name}")
    print("="*60)
    print("Press Ctrl+C to stop")
    print("-"*60)

    def audio_callback(indata, frames, time_info, status):
        if status:
            print(f"Status: {status}")

        rms = np.sqrt(np.mean(indata**2))
        bar_length = int(rms * 100)
        bar = '#' * min(bar_length, 50)

        # Color coding for terminal (if supported)
        if rms > 0.1:
            level = "HIGH"
        elif rms > 0.01:
            level = "GOOD"
        elif rms > 0.001:
            level = "LOW "
        else:
            level = "NONE"

        print(f"\r[{level}] [{bar:<50}] {rms:.4f}", end='', flush=True)

    try:
        with sd.InputStream(
            callback=audio_callback,
            channels=1,
            samplerate=sample_rate,
            device=device_index
        ):
            while True:
                time.sleep(0.1)
    except KeyboardInterrupt:
        print("\n\nMonitoring stopped.")
    except Exception as e:
        print(f"\n*** ERROR: {e} ***")

def main():
    parser = argparse.ArgumentParser(description='Test microphone functionality')
    parser.add_argument('--device', type=int, default=None,
                       help='Audio input device index (default: system default)')
    parser.add_argument('--duration', type=int, default=5,
                       help='Test duration in seconds (default: 5)')
    parser.add_argument('--sample-rate', type=int, default=16000,
                       help='Sample rate in Hz (default: 16000)')
    parser.add_argument('--list', action='store_true',
                       help='List all available audio devices')
    parser.add_argument('--monitor', action='store_true',
                       help='Continuously monitor audio levels')

    args = parser.parse_args()

    print("\nMICROPHONE TEST UTILITY")
    print("=" * 60)

    # Always list devices first
    input_devices = list_audio_devices()

    if args.list:
        # Just list devices and exit
        sys.exit(0)

    if not input_devices:
        print("\nCannot proceed without input devices.")
        sys.exit(1)

    # Validate device selection
    if args.device is not None and args.device not in input_devices:
        print(f"\n*** ERROR: Device #{args.device} is not a valid input device ***")
        print(f"Available input devices: {input_devices}")
        sys.exit(1)

    if args.monitor:
        # Continuous monitoring mode
        continuous_monitor(args.device, args.sample_rate)
    else:
        # Single test mode
        success = test_audio_input(args.device, args.duration, args.sample_rate)

        if success:
            print("\n" + "="*60)
            print("✓ Microphone test completed successfully!")
            print("="*60)
        else:
            print("\n" + "="*60)
            print("✗ Microphone test failed")
            print("="*60)
            sys.exit(1)

if __name__ == "__main__":
    main()