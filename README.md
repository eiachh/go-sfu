# go-sfu

A minimal WebRTC SFU (Selective Forwarding Unit) built in Go with
[Pion WebRTC](https://github.com/pion/webrtc).

Three independent processes connected via **WebRTC** (VP8 / RTP) with
**HTTP** signaling for SDP exchange:

| Process    | Role                                                                |
|------------|---------------------------------------------------------------------|
| **source** | FFmpeg encodes VP8, Pion sends via WebRTC to SFU                   |
| **sfu**    | Pion receives source track, forwards RTP to all viewer connections  |
| **viewer** | Pion receives VP8 track, FFmpeg decodes, SDL2 displays              |

## Prerequisites

| Tool       | Version    | Notes                                         |
|------------|------------|-----------------------------------------------|
| **Go**     | 1.25.0+    | CGo enabled (for SDL2)                        |
| **FFmpeg** | 6.x / 7.x | With `libvpx` for VP8 encode/decode           |
| **SDL2**   | 2.x        | Install the *-dev* package for your distro    |

### Debian / Ubuntu

```bash
sudo apt-get install ffmpeg libsdl2-dev
```

### macOS

```bash
brew install ffmpeg sdl2
```

## Build

```bash
go build -o bin/sfu    ./cmd/sfu
go build -o bin/source ./cmd/source
go build -o bin/viewer ./cmd/viewer
```

## Run (3 terminals)

```bash
# Terminal 1 - start the SFU
./bin/sfu

# Terminal 2 - start the source (FFmpeg wrapper)
./bin/source                   # test pattern 640x480
./bin/source video.mp4         # or a file

# Terminal 3 - start the viewer
./bin/viewer
```

Press **Escape** or **Q** in the SDL window to quit the viewer.

## Project Structure

```
go-sfu/
├── cmd/
│   ├── source/main.go    # FFmpeg VP8/IVF pipe -> Pion WebRTC -> SFU
│   ├── sfu/main.go       # WebRTC SFU + HTTP SDP signaling
│   └── viewer/main.go    # Pion WebRTC -> VP8 depacketize -> FFmpeg decode -> SDL
├── ffmpeg/
│   └── ffmpeg.go         # FFmpeg process wrapper (raw RGBA utility)
├── display/
│   └── display.go        # SDL2 window with dynamic resolution
├── go.mod
└── README.md
```

## Architecture

```
  ┌──────────┐ VP8/IVF  ┌──────────┐  WebRTC    ┌──────────┐  WebRTC    ┌──────────┐
  │ FFmpeg   │ -------> │  source  │ <--------> │   SFU    │ <--------> │  viewer  │
  │ (encode) │  pipe    │  (Pion)  │  VP8/RTP   │  (Pion)  │  VP8/RTP  │  (Pion)  │
  └──────────┘          └──────────┘  + SDP/HTTP └──────────┘  + SDP/HTTP └────┬─────┘
                                                                               │ VP8
                                                                          ┌────▼─────┐
                                                                          │ FFmpeg   │
                                                                          │ (decode) │
                                                                          └────┬─────┘
                                                                               │ RGBA
                                                                          ┌────▼─────┐
                                                                          │  SDL2    │
                                                                          └──────────┘
```

### Signaling (HTTP)

SDP exchange uses two HTTP endpoints on the SFU (default `:8080`):

| Endpoint  | Method | Request          | Response         |
|-----------|--------|------------------|------------------|
| `/source` | POST   | SDP offer (JSON) | SDP answer (JSON)|
| `/viewer` | POST   | SDP offer (JSON) | SDP answer (JSON)|

ICE candidates are gathered locally (no STUN/TURN needed for localhost).

### Data flow

1. **Source** launches FFmpeg encoding a test pattern (or file) as VP8 in IVF
   container format, piped to stdout. It reads IVF frames, creates a Pion
   `TrackLocalStaticSample`, and calls `WriteSample()` for each frame. SDP
   is exchanged with the SFU via `POST /source`.

2. **SFU** receives the source VP8 track via `OnTrack`. It creates a
   `TrackLocalStaticRTP` and starts a goroutine that reads every RTP packet
   from the source and writes it to the local track. When a viewer connects,
   the same local track is added to the viewer PeerConnection -- Pion
   handles the fan-out automatically.

3. **Viewer** creates a recvonly PeerConnection, exchanges SDP via
   `POST /viewer`, and receives the VP8 track. A goroutine reads RTP
   packets, depacketizes VP8 (strips the RTP payload descriptor), and
   reassembles complete VP8 frames using the RTP marker bit. Frames are
   piped in IVF format to a local FFmpeg process that decodes VP8 to
   raw RGBA. The main thread reads decoded frames and renders with SDL2.
