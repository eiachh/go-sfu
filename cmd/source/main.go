// Command source is the FFmpeg wrapper / media backend.
//
// It launches FFmpeg to encode a test pattern (or file) as VP8 in IVF
// container format, reads the VP8 frames, and sends them over WebRTC to
// the SFU.
//
// Usage:
//
//	go run ./cmd/source                              # test pattern
//	go run ./cmd/source -sfu http://localhost:8080 video.mp4
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
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func main() {
	sfuURL := flag.String("sfu", "http://localhost:8080", "SFU HTTP base URL")
	width := flag.Int("w", 640, "video width")
	height := flag.Int("h", 480, "video height")
	fps := flag.Int("fps", 30, "frame rate")
	flag.Parse()

	input := "testsrc"
	if flag.NArg() > 0 {
		input = flag.Arg(0)
	}

	// ---- start FFmpeg: encode to VP8, output IVF to pipe ----
	ffCmd := buildFFmpegCmd(input, *width, *height, *fps)
	log.Printf("source: starting FFmpeg  %v", ffCmd.Args)

	stdout, err := ffCmd.StdoutPipe()
	if err != nil {
		log.Fatalf("source: pipe: %v", err)
	}
	ffCmd.Stderr = nil // discard FFmpeg logs
	if err := ffCmd.Start(); err != nil {
		log.Fatalf("source: ffmpeg start: %v", err)
	}
	defer ffCmd.Process.Kill()

	// Skip the 32-byte IVF file header.
	var ivfHdr [32]byte
	if _, err := io.ReadFull(stdout, ivfHdr[:]); err != nil {
		log.Fatalf("source: read IVF header: %v", err)
	}

	// ---- create WebRTC PeerConnection ----
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatalf("source: new PC: %v", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8},
		"video", "source",
	)
	if err != nil {
		log.Fatalf("source: new track: %v", err)
	}

	rtpSender, err := pc.AddTrack(track)
	if err != nil {
		log.Fatalf("source: add track: %v", err)
	}
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := rtpSender.Read(buf); err != nil {
				return
			}
		}
	}()

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("source: connection: %s", s)
	})

	// ---- SDP exchange with SFU ----
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatalf("source: create offer: %v", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		log.Fatalf("source: set local desc: %v", err)
	}
	<-gatherDone

	answer := doSDPExchange(*sfuURL+"/source", *pc.LocalDescription())
	if err := pc.SetRemoteDescription(answer); err != nil {
		log.Fatalf("source: set remote desc: %v", err)
	}
	log.Println("source: SDP exchange complete, streaming...")

	fmt.Println("===========================================")
	fmt.Printf("  Source streaming VP8 %dx%d @ %d fps\n", *width, *height, *fps)
	fmt.Println("===========================================")

	// ---- read IVF frames and write to WebRTC track ----
	frameDur := time.Second / time.Duration(*fps)
	for {
		data, err := readIVFFrame(stdout)
		if err != nil {
			log.Printf("source: stream ended: %v", err)
			return
		}
		if err := track.WriteSample(media.Sample{Data: data, Duration: frameDur}); err != nil {
			log.Printf("source: write sample: %v", err)
			return
		}
	}
}

// readIVFFrame reads one IVF frame: 12-byte header (size:4 LE, ts:8 LE) + data.
func readIVFFrame(r io.Reader) ([]byte, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	size := binary.LittleEndian.Uint32(hdr[0:4])
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	return data, nil
}

// doSDPExchange POSTs an SDP offer to the given URL and returns the answer.
func doSDPExchange(url string, offer webrtc.SessionDescription) webrtc.SessionDescription {
	body, _ := json.Marshal(offer)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Fatalf("source: SDP POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Fatalf("source: SDP POST %d: %s", resp.StatusCode, b)
	}
	var answer webrtc.SessionDescription
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		log.Fatalf("source: decode answer: %v", err)
	}
	return answer
}

func buildFFmpegCmd(input string, w, h, fps int) *exec.Cmd {
	size := fmt.Sprintf("%dx%d", w, h)
	rate := fmt.Sprintf("%d", fps)

	switch input {
	case "testsrc", "testsrc2", "smptebars":
		return exec.Command("ffmpeg",
			"-f", "lavfi",
			"-i", fmt.Sprintf("%s=size=%s:rate=%s", input, size, rate),
			"-c:v", "libvpx", "-b:v", "1M",
			"-cpu-used", "5", "-deadline", "realtime",
			"-g", rate, "-keyint_min", rate,
			"-f", "ivf", "-v", "error",
			"pipe:1",
		)
	default:
		return exec.Command("ffmpeg",
			"-re", "-i", input,
			"-vf", fmt.Sprintf("scale=%s", size),
			"-r", rate,
			"-c:v", "libvpx", "-b:v", "1M",
			"-cpu-used", "5", "-deadline", "realtime",
			"-g", rate, "-keyint_min", rate,
			"-f", "ivf", "-v", "error",
			"pipe:1",
		)
	}
}
