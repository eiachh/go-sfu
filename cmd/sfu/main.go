// Command sfu is a minimal WebRTC Selective Forwarding Unit.
//
// It exposes two HTTP endpoints for SDP signaling:
//
//	POST /source  — source sends offer, receives answer
//	POST /viewer  — viewer sends offer, receives answer
//
// The SFU reads RTP from the source track and writes it to a local track
// that is shared across all viewer PeerConnections (zero-copy fan-out).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/pion/webrtc/v4"
)

var (
	mu          sync.Mutex
	sourceTrack *webrtc.TrackLocalStaticRTP
	sourceReady = make(chan struct{})
	sourceOnce  sync.Once
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	flag.Parse()

	http.HandleFunc("/source", handleSource)
	http.HandleFunc("/viewer", handleViewer)

	fmt.Println("===========================================")
	fmt.Println("  SFU (WebRTC)")
	fmt.Printf("  HTTP signaling on %s\n", *addr)
	fmt.Println("  POST /source  — source SDP exchange")
	fmt.Println("  POST /viewer  — viewer SDP exchange")
	fmt.Println("===========================================")

	log.Fatal(http.ListenAndServe(*addr, nil))
}

// handleSource accepts an SDP offer from the source, creates an answer, and
// starts forwarding the source's video track to a local static RTP track.
func handleSource(w http.ResponseWriter, r *http.Request) {
	var offer webrtc.SessionDescription
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("sfu: source track arrived  codec=%s", remote.Codec().MimeType)

		local, err := webrtc.NewTrackLocalStaticRTP(
			remote.Codec().RTPCodecCapability, "video", "source")
		if err != nil {
			log.Printf("sfu: create local track: %v", err)
			return
		}

		mu.Lock()
		sourceTrack = local
		mu.Unlock()
		sourceOnce.Do(func() { close(sourceReady) })

		// Forward every RTP packet from source → local track.
		// local track fans out to all viewer PeerConnections.
		buf := make([]byte, 1500)
		for {
			n, _, err := remote.Read(buf)
			if err != nil {
				log.Printf("sfu: source read: %v", err)
				return
			}
			if _, err := local.Write(buf[:n]); err != nil {
				log.Printf("sfu: local write: %v", err)
				return
			}
		}
	})

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("sfu: source connection: %s", s)
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	<-gatherDone

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pc.LocalDescription())
	log.Printf("sfu: source SDP exchange complete")
}

// handleViewer accepts an SDP offer from a viewer, adds the source track,
// and returns an answer.
func handleViewer(w http.ResponseWriter, r *http.Request) {
	// Block until the source has connected and published a track.
	select {
	case <-sourceReady:
	default:
		log.Println("sfu: viewer waiting for source track...")
		<-sourceReady
	}

	var offer webrtc.SessionDescription
	if err := json.NewDecoder(r.Body).Decode(&offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	mu.Lock()
	track := sourceTrack
	mu.Unlock()

	rtpSender, err := pc.AddTrack(track)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Drain RTCP from the viewer (needed for Pion to work correctly).
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := rtpSender.Read(buf); err != nil {
				return
			}
		}
	}()

	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		log.Printf("sfu: viewer connection: %s", s)
	})

	if err := pc.SetRemoteDescription(offer); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	<-gatherDone

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(pc.LocalDescription())
	log.Printf("sfu: viewer SDP exchange complete")
}
