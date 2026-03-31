// Command viewer connects to the SFU over WebRTC, receives a VP8 video
// track, decodes it via FFmpeg, and displays raw RGBA frames in SDL2.
//
// Usage:
//
//	go run ./cmd/viewer
//	go run ./cmd/viewer -sfu http://localhost:8080 -w 640 -h 480
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/go-sfu/go-sfu/display"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

func init() { runtime.LockOSThread() }

func main() {
	sfuURL := flag.String("sfu", "http://localhost:8080", "SFU HTTP base URL")
	width := flag.Int("w", 640, "expected video width")
	height := flag.Int("h", 480, "expected video height")
	flag.Parse()

	// ---- create WebRTC PeerConnection ----
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatalf("viewer: new PC: %v", err)
	}

	pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly})

	// Channel to receive the remote track once it arrives.
	trackCh := make(chan *webrtc.TrackRemote, 1)
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("viewer: track arrived  codec=%s", t.Codec().MimeType)
		trackCh <- t
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("viewer: connection: %s", s)
	})

	// ---- SDP exchange with SFU ----
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatalf("viewer: create offer: %v", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Fatalf("viewer: set local desc: %v", err)
	}
	<-gatherDone

	answer := doSDPExchange(*sfuURL+"/viewer", *pc.LocalDescription())
	if err := pc.SetRemoteDescription(answer); err != nil {
		log.Fatalf("viewer: set remote desc: %v", err)
	}
	log.Println("viewer: SDP exchange complete, waiting for track...")

	// ---- wait for video track ----
	track := <-trackCh

	// ---- start FFmpeg decoder: VP8/IVF → raw RGBA ----
	decCmd := exec.Command("ffmpeg",
		"-f", "ivf", "-i", "pipe:0",
		"-pix_fmt", "rgba",
		"-f", "rawvideo",
		"-v", "error",
		"pipe:1",
	)
	decStdin, _ := decCmd.StdinPipe()
	decStdout, _ := decCmd.StdoutPipe()
	decCmd.Stderr = nil
	if err := decCmd.Start(); err != nil {
		log.Fatalf("viewer: ffmpeg decode start: %v", err)
	}
	defer decCmd.Process.Kill()

	// Write IVF file header to FFmpeg stdin.
	writeIVFHeader(decStdin, uint16(*width), uint16(*height))

	// Background goroutine: read RTP → depacketize VP8 → write IVF to FFmpeg.
	go func() {
		defer decStdin.Close()
		depacketizer := &codecs.VP8Packet{}
		var frame []byte
		var pts uint64

		for {
			pkt, _, err := track.ReadRTP()
			if err != nil {
				log.Printf("viewer: RTP read: %v", err)
				return
			}
			raw, err := depacketizer.Unmarshal(pkt.Payload)
			if err != nil {
				continue
			}
			frame = append(frame, raw...)

			if pkt.Marker { // end of VP8 frame
				writeIVFFrame(decStdin, frame, pts)
				pts++
				frame = frame[:0]
			}
		}
	}()

	// Background goroutine: read RGBA frames from FFmpeg → channel.
	frameSize := *width * *height * 4
	frameCh := make(chan []byte, 2)
	go func() {
		defer close(frameCh)
		for {
			buf := make([]byte, frameSize)
			if _, err := io.ReadFull(decStdout, buf); err != nil {
				log.Printf("viewer: FFmpeg decode read: %v", err)
				return
			}
			select {
			case frameCh <- buf:
			default: // drop if render is behind
			}
		}
	}()

	// ---- wait for first decoded frame (still on main thread, SDL not up) ----
	fmt.Println("===========================================")
	fmt.Println("  Viewer (WebRTC → VP8 → SDL)")
	fmt.Println("  Press Escape/Q to quit")
	fmt.Println("  Waiting for first decoded frame...")
	fmt.Println("===========================================")

	first, ok := <-frameCh
	if !ok {
		log.Fatal("viewer: no frames decoded")
	}
	log.Printf("viewer: first frame decoded  %dx%d", *width, *height)

	// ---- create SDL window and render ----
	win, err := display.New("go-sfu viewer", *width, *height)
	if err != nil {
		log.Fatalf("viewer: %v", err)
	}
	defer win.Destroy()
	win.UpdateFrame(first)

	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	frames := 0

	for range ticker.C {
		if win.PollQuit() {
			log.Printf("viewer: quit after %d frames", frames)
			break
		}
		// Drain channel, display latest.
		var latest []byte
	drain:
		for {
			select {
			case f, ok := <-frameCh:
				if !ok {
					log.Printf("viewer: stream ended after %d frames", frames)
					return
				}
				latest = f
			default:
				break drain
			}
		}
		if latest != nil {
			win.UpdateFrame(latest)
			frames++
		}
	}
}

// doSDPExchange POSTs an SDP offer and returns the answer.
func doSDPExchange(url string, offer webrtc.SessionDescription) webrtc.SessionDescription {
	body, _ := json.Marshal(offer)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("viewer: SDP POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("viewer: SDP POST %d: %s", resp.StatusCode, b)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		log.Fatalf("viewer: decode answer: %v", err)
	}
	return answer
}

// writeIVFHeader writes a 32-byte IVF file header.
func writeIVFHeader(w io.Writer, width, height uint16) {
	var hdr [32]byte
	copy(hdr[0:4], "DKIF")
	binary.LittleEndian.PutUint16(hdr[4:6], 0)    // version
	binary.LittleEndian.PutUint16(hdr[6:8], 32)   // header size
	copy(hdr[8:12], "VP80")                        // codec FourCC
	binary.LittleEndian.PutUint16(hdr[12:14], width)
	binary.LittleEndian.PutUint16(hdr[14:16], height)
	binary.LittleEndian.PutUint32(hdr[16:20], 30)  // timebase den
	binary.LittleEndian.PutUint32(hdr[20:24], 1)   // timebase num
	binary.LittleEndian.PutUint32(hdr[24:28], 0)   // frame count (unknown)
	w.Write(hdr[:])
}

// writeIVFFrame writes one IVF frame: 12-byte header + data.
func writeIVFFrame(w io.Writer, data []byte, pts uint64) {
	var hdr [12]byte
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(len(data)))
	binary.LittleEndian.PutUint64(hdr[4:12], pts)
	w.Write(hdr[:])
	w.Write(data)
}
