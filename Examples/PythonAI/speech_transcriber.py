#!/usr/bin/env python3
"""
Speech Transcriber using Whisper
Captures audio from USB microphone and transcribes it using OpenAI's Whisper model
"""

import sys
import os
import time
import numpy as np
import sounddevice as sd
import queue
import threading
import whisper
import warnings

# Suppress FP16 warning on CPU
warnings.filterwarnings("ignore", message="FP16 is not supported on CPU; using FP32 instead")

class SpeechTranscriber:
    def __init__(self, model_size="base", device_index=None):
        """
        Initialize the speech transcriber with Whisper model

        Args:
            model_size: Size of Whisper model ('tiny', 'base', 'small', 'medium', 'large')
            device_index: Index of the audio device to use (None for default)
        """
        print(f"Loading Whisper model '{model_size}'...")
        self.model = whisper.load_model(model_size)
        print(f"Model loaded successfully!")

        self.audio_queue = queue.Queue()
        self.device_index = device_index
        self.sample_rate = 16000  # Whisper expects 16kHz audio
        self.channels = 1
        self.blocksize = int(self.sample_rate * 0.1)  # 100ms blocks
        self.recording = False
        self.audio_buffer = []
        self.silence_threshold = 0.01
        self.silence_duration = 1.5  # seconds of silence before processing
        self.min_audio_duration = 0.5  # minimum audio duration to process

    def list_audio_devices(self):
        """List all available audio input devices"""
        print("\nAvailable audio input devices:")
        print("-" * 50)
        devices = sd.query_devices()
        for i, device in enumerate(devices):
            if device['max_input_channels'] > 0:
                default_marker = " (DEFAULT)" if i == sd.default.device[0] else ""
                print(f"Device {i}: {device['name']}{default_marker}")
                print(f"  Channels: {device['max_input_channels']}")
                print(f"  Sample Rate: {device['default_samplerate']}")
        print("-" * 50)

    def audio_callback(self, indata, frames, time_info, status):
        """Callback for audio stream"""
        if status:
            print(f"Audio callback status: {status}", file=sys.stderr)

        # Add audio to queue for processing
        self.audio_queue.put(indata.copy())

    def process_audio_queue(self):
        """Process audio from the queue in a separate thread"""
        silence_samples = 0
        silence_threshold_samples = int(self.silence_duration * self.sample_rate)

        while self.recording:
            try:
                # Get audio chunk from queue
                audio_chunk = self.audio_queue.get(timeout=0.1)

                # Calculate RMS (volume level)
                rms = np.sqrt(np.mean(audio_chunk**2))

                if rms > self.silence_threshold:
                    # Voice detected, add to buffer
                    self.audio_buffer.append(audio_chunk)
                    silence_samples = 0

                    # Visual feedback
                    bar_length = int(rms * 100)
                    bar = '#' * min(bar_length, 50)
                    print(f"\rRecording: [{bar:<50}] Volume: {rms:.4f}", end='', flush=True)

                else:
                    # Silence detected
                    if len(self.audio_buffer) > 0:
                        self.audio_buffer.append(audio_chunk)
                        silence_samples += len(audio_chunk)

                        # Check if we have enough silence to process
                        if silence_samples >= silence_threshold_samples:
                            self.process_buffer()
                            silence_samples = 0
                    else:
                        print(f"\rListening... (speak into the microphone)", end='', flush=True)

            except queue.Empty:
                continue
            except Exception as e:
                print(f"\nError in audio processing: {e}", file=sys.stderr)

    def process_buffer(self):
        """Process the accumulated audio buffer with Whisper"""
        if len(self.audio_buffer) == 0:
            return

        # Concatenate all audio chunks
        audio_data = np.concatenate(self.audio_buffer, axis=0).flatten()

        # Check minimum duration
        duration = len(audio_data) / self.sample_rate
        if duration < self.min_audio_duration:
            self.audio_buffer = []
            return

        print(f"\n\nProcessing {duration:.1f} seconds of audio...")

        try:
            # Transcribe with Whisper
            result = self.model.transcribe(
                audio_data,
                language=None,  # Auto-detect language
                fp16=False,  # Use FP32 for CPU
                verbose=False
            )

            # Print results
            text = result['text'].strip()
            if text:
                print(f"\n{'='*60}")
                print(f"Transcription: {text}")
                if result.get('language'):
                    print(f"Language: {result['language']}")
                print(f"{'='*60}\n")

        except Exception as e:
            print(f"\nError during transcription: {e}", file=sys.stderr)

        # Clear buffer for next recording
        self.audio_buffer = []

    def run(self):
        """Main loop for audio capture and transcription"""
        # List available devices
        self.list_audio_devices()

        # Select device
        if self.device_index is None:
            print(f"\nUsing default audio input device")
        else:
            print(f"\nUsing audio input device {self.device_index}")

        print("\nStarting speech transcription...")
        print("Speak into the microphone. The system will automatically detect speech and transcribe it.")
        print("Press Ctrl+C to quit")
        print("=" * 60)

        self.recording = True

        # Start audio processing thread
        processing_thread = threading.Thread(target=self.process_audio_queue)
        processing_thread.start()

        try:
            # Start audio stream
            with sd.InputStream(
                device=self.device_index,
                channels=self.channels,
                samplerate=self.sample_rate,
                blocksize=self.blocksize,
                callback=self.audio_callback
            ):
                # Keep the stream running
                while self.recording:
                    time.sleep(0.1)

        except KeyboardInterrupt:
            print("\n\nShutting down...")
        except Exception as e:
            print(f"\nError: {e}", file=sys.stderr)
        finally:
            self.cleanup(processing_thread)

    def cleanup(self, processing_thread):
        """Clean up resources"""
        self.recording = False

        # Process any remaining audio
        if len(self.audio_buffer) > 0:
            print("\nProcessing remaining audio...")
            self.process_buffer()

        # Wait for processing thread to finish
        if processing_thread and processing_thread.is_alive():
            processing_thread.join(timeout=2)

        print("\nSpeech transcriber stopped.")

def main():
    """Main function"""
    print("USB Microphone Speech Transcriber")
    print("=" * 40)

    # Parse command line arguments
    import argparse
    parser = argparse.ArgumentParser(description='Speech transcription using Whisper')
    parser.add_argument('--model', type=str, default='base',
                       choices=['tiny', 'base', 'small', 'medium', 'large'],
                       help='Whisper model size (default: base)')
    parser.add_argument('--device', type=int, default=None,
                       help='Audio input device index (default: system default)')
    parser.add_argument('--list-devices', action='store_true',
                       help='List available audio devices and exit')

    args = parser.parse_args()

    # Create transcriber
    transcriber = SpeechTranscriber(
        model_size=args.model,
        device_index=args.device
    )

    # List devices and exit if requested
    if args.list_devices:
        transcriber.list_audio_devices()
        sys.exit(0)

    # Run transcriber
    transcriber.run()

if __name__ == "__main__":
    main()