# USB Microphone Speech Transcriber

A Python application that captures audio from a USB microphone and transcribes speech to text using OpenAI's Whisper model. Designed to run in containerd environments with proper audio device access.

## Features

- Real-time speech-to-text transcription using OpenAI Whisper
- Automatic voice activity detection (VAD)
- Support for multiple Whisper model sizes (tiny, base, small, medium, large)
- USB microphone support with device selection
- Multi-language support with automatic language detection
- Visual feedback showing audio input levels
- Containerized deployment with audio entitlements

## Requirements

- Python 3.7 or higher
- USB microphone connected to the system
- Docker/containerd for containerized deployment
- ALSA or PulseAudio for audio system access

## Installation

### Option 1: Docker/Containerd (Recommended for Production)

1. **Clone or download this project:**
   ```bash
   cd /path/to/your/project
   ```

2. **Build and run with Docker Compose:**
   ```bash
   # Build and run the container
   docker-compose up --build
   ```

3. **Alternative Docker commands:**
   ```bash
   # Build the image
   docker build -t speech-transcriber .

   # Run with USB audio device access
   docker run --privileged \
     --device=/dev/snd:/dev/snd \
     --device=/dev/bus/usb:/dev/bus/usb \
     speech-transcriber
   ```

### Option 2: Local Python Installation

1. **Clone or download this project:**
   ```bash
   cd /path/to/your/project
   ```

2. **Create a virtual environment (recommended):**
   ```bash
   python3 -m venv venv
   source venv/bin/activate  # On macOS/Linux
   ```

3. **Install dependencies:**
   ```bash
   pip install -r requirements.txt
   ```

## Usage

### Docker/Containerd Usage

1. **Run with Docker Compose:**
   ```bash
   docker-compose up
   ```

2. **Run with specific audio device:**
   ```bash
   # List available devices first
   docker-compose run speech-transcriber python speech_transcriber.py --list-devices

   # Run with specific device (e.g., device 2)
   docker-compose run speech-transcriber python speech_transcriber.py --device 2
   ```

3. **Stop the application:**
   ```bash
   docker-compose down
   ```

### Local Python Usage

1. **List available audio devices:**
   ```bash
   python speech_transcriber.py --list-devices
   ```

2. **Run the speech transcriber:**
   ```bash
   # Use default microphone
   python speech_transcriber.py

   # Use specific device
   python speech_transcriber.py --device 2

   # Use different Whisper model size
   python speech_transcriber.py --model small
   ```

### General Usage

1. **Position your USB microphone** appropriately
2. **Speak clearly** - the application will automatically detect when you start and stop speaking
3. **Wait for transcription** - after detecting silence, the audio will be processed and transcribed
4. **View results** - transcriptions appear in the console with detected language

### Controls
- Press `Ctrl+C` to quit the application

## Command Line Options

```bash
python speech_transcriber.py [OPTIONS]

Options:
  --model {tiny,base,small,medium,large}
                        Whisper model size (default: base)
  --device DEVICE       Audio input device index (default: system default)
  --list-devices        List available audio devices and exit
```

## How It Works

1. **Audio Capture:** Continuously captures audio from the USB microphone at 16kHz
2. **Voice Activity Detection:** Monitors audio levels to detect speech
3. **Buffering:** Accumulates audio while speech is detected
4. **Silence Detection:** Waits for 1.5 seconds of silence to mark end of speech
5. **Transcription:** Processes accumulated audio with Whisper model
6. **Output:** Displays transcribed text with language detection

## Edge Deployment

This application is configured for edge deployment with containerd:

- **edge.json:** Contains audio entitlement for microphone access
- **Containerized:** Fully containerized with all dependencies
- **Device Access:** Proper USB and audio device mounting

## Troubleshooting

### Docker/Container Issues

#### No Audio Device Found
- Ensure USB microphone is connected before starting container
- Check device permissions: `ls -la /dev/snd/`
- Verify USB device: `lsusb` to see if microphone is detected
- Try running with `--privileged` flag

#### Permission Denied
- Add user to `audio` group: `sudo usermod -a -G audio $USER`
- Restart system or logout/login for group changes to take effect

### Audio Issues

#### No Sound Input
- Test microphone locally first: `arecord -l`
- Check volume levels: `alsamixer` or `pavucontrol`
- Ensure microphone is not muted
- Try different device index with `--device` parameter

#### Poor Transcription Quality
- Speak clearly and at normal volume
- Reduce background noise
- Try larger Whisper model: `--model medium` or `--model large`
- Ensure microphone is positioned correctly

### Model Issues

#### Slow Performance
- Use smaller model: `--model tiny` for faster processing
- Consider GPU acceleration if available
- Adjust silence detection threshold in code

#### Out of Memory
- Use smaller Whisper model
- Reduce audio buffer size in code
- Ensure sufficient system memory

## Technical Details

- **Audio Format:** 16kHz, mono, float32
- **VAD Method:** RMS-based threshold detection
- **Silence Duration:** 1.5 seconds before processing
- **Min Audio Duration:** 0.5 seconds
- **Model Options:** tiny (~39M), base (~74M), small (~244M), medium (~769M), large (~1550M)
- **Languages:** 100+ languages with automatic detection

## Customization

### Adjust Voice Detection Sensitivity

Edit `speech_transcriber.py`:

```python
self.silence_threshold = 0.01  # Increase for noisy environments
self.silence_duration = 1.5    # Seconds of silence before processing
self.min_audio_duration = 0.5  # Minimum speech duration
```

### Change Audio Parameters

```python
self.sample_rate = 16000  # Whisper requires 16kHz
self.channels = 1         # Mono audio
self.blocksize = 1600     # 100ms blocks
```

## Testing Microphone

Use the included test script:

```bash
python test_microphone.py
```

This will help verify microphone functionality before running the main application.

## Performance Considerations

- **Model Size vs Speed:** Smaller models (tiny, base) are faster but less accurate
- **CPU vs GPU:** GPU acceleration significantly improves performance
- **Network:** First run downloads model (~39MB to ~1.5GB depending on size)
- **Memory:** Larger models require more RAM (up to 10GB for large model)

## License

This project is open source and available under the MIT License.